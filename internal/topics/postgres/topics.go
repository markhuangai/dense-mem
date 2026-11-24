package postgres

import (
	"context"
	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
	"sort"
)

func (s *Store) NewTopicReader() func(context.Context, *gorm.DB, string, ontology.TopicReadInput) (ontology.TopicReadResult, error) {
	return s.ReadEvidenceTopics
}

func (s *Store) ReadEvidenceTopics(ctx context.Context, tx *gorm.DB, teamID string, input ontology.TopicReadInput) (ontology.TopicReadResult, error) {
	result := ontology.TopicReadResult{Topics: []ontology.EvidenceTopic{}, Rejected: map[string]string{}}
	fence, err := sharedScope(ctx, tx, teamID)
	if err != nil {
		return result, err
	}
	result.SpaceID, result.Generation = fence.SpaceID, fence.Generation
	if input.SpaceID != "" && input.SpaceID != fence.SpaceID {
		return result, ontology.ErrTopicUnavailable
	}
	var records []ontology.Record
	if input.TopicIDs != nil {
		heads, loadErr := loadHeads(tx, fence, input.TopicIDs, nil)
		if loadErr != nil {
			return result, loadErr
		}
		for _, id := range input.TopicIDs {
			topic, ok := heads[id]
			if !ok || topic.Kind != ontology.Topic || topic.Retired {
				result.Rejected[id] = "unavailable"
				continue
			}
			records = append(records, topic)
		}
	} else if len(input.EvidenceIDs) > 0 && input.MaxMembers == 0 {
		records, err = recallRecords(tx, fence, `head.kind='topic' AND head.record_id IN (
			SELECT assignment.topic_id FROM topic_record_heads assignment
			WHERE assignment.team_id=head.team_id AND assignment.shared_space_id=head.shared_space_id
			  AND assignment.space_generation=head.space_generation AND assignment.kind='assignment'
			  AND NOT assignment.retired AND assignment.evidence_id=ANY(?::uuid[]))`, []any{pq.Array(input.EvidenceIDs)}, ontology.MaxPageSize)
		if err != nil {
			return result, err
		}
	} else if len(input.EvidenceIDs) > 0 {
		ids, err := smallTopicsForEvidence(tx, fence, input)
		if err != nil {
			return result, err
		}
		if len(ids) == 0 {
			return result, nil
		}
		records, err = recallRecords(tx, fence, `head.kind='topic' AND head.record_id=ANY(?::uuid[])`, []any{pq.Array(ids)}, ontology.MaxPageSize)
		if err != nil {
			return result, err
		}
	}
	views, err := s.currentViews(tx, fence, records)
	if err != nil {
		return result, err
	}
	topics := map[string]int{}
	ids := []string{}
	for _, view := range views {
		if !view.Current || view.Definition == nil {
			result.Rejected[view.ID] = view.StaleReason
			continue
		}
		name := view.Definition.Label
		if name == "" {
			name = view.Definition.Key
		}
		topics[view.ID] = len(result.Topics)
		ids = append(ids, view.ID)
		result.Topics = append(result.Topics, ontology.EvidenceTopic{ID: view.ID, Version: view.Version, Name: name, Description: view.Definition.Description, EvidenceIDs: []string{}, AssignmentIDs: []string{}})
	}
	if input.TopicIDs != nil && len(result.Rejected) > 0 {
		return result, ontology.ErrTopicUnavailable
	}
	if len(ids) == 0 {
		return result, nil
	}
	assignments, err := recallRecords(tx, fence, `head.kind='assignment' AND head.topic_id=ANY(?::uuid[])`, []any{pq.Array(ids)}, ontology.MaxDependencyRecords)
	if err != nil {
		return result, err
	}
	views, err = s.currentViews(tx, fence, assignments)
	if err != nil {
		return result, err
	}
	for _, view := range views {
		if !view.Current || view.Assignment == nil || view.Assignment.Source.Kind != ontology.EvidenceSource {
			result.Rejected[view.ID] = view.StaleReason
			continue
		}
		if index, ok := topics[view.Assignment.DefinitionID]; ok {
			topic := &result.Topics[index]
			topic.EvidenceIDs = append(topic.EvidenceIDs, view.Assignment.Source.ID)
			topic.AssignmentIDs = append(topic.AssignmentIDs, view.ID)
		}
	}
	sort.Slice(result.Topics, func(i, j int) bool { return result.Topics[i].ID < result.Topics[j].ID })
	for index := range result.Topics {
		sort.Strings(result.Topics[index].EvidenceIDs)
		sort.Strings(result.Topics[index].AssignmentIDs)
	}
	return result, nil
}

func smallTopicsForEvidence(tx *gorm.DB, fence scope, input ontology.TopicReadInput) ([]string, error) {
	var ids []string
	if input.MaxMembers < 1 || input.MaxMembers > ontology.MaxPageSize {
		return nil, ontology.ErrInvalid
	}
	err := tx.Raw(`WITH scope AS (SELECT ?::uuid AS team_id,?::uuid AS shared_space_id,?::bigint AS space_generation),
		seeds AS (SELECT unnest(?::uuid[]) AS evidence_id),
		links AS MATERIALIZED (
			SELECT seeds.evidence_id,item.topic_id FROM seeds CROSS JOIN scope
			CROSS JOIN LATERAL (
				SELECT head.topic_id FROM topic_record_heads AS head
				WHERE head.team_id=scope.team_id AND head.shared_space_id=scope.shared_space_id
				  AND head.space_generation=scope.space_generation AND head.evidence_id=seeds.evidence_id
				  AND head.kind='assignment' AND NOT head.retired
				ORDER BY head.topic_id LIMIT ?
			) AS item
		), sizes AS MATERIALIZED (
			SELECT selected.topic_id,(SELECT count(*) FROM (
				SELECT 1 FROM topic_record_heads AS peer
				WHERE peer.team_id=scope.team_id AND peer.shared_space_id=scope.shared_space_id
				  AND peer.space_generation=scope.space_generation AND peer.topic_id=selected.topic_id
				  AND peer.kind='assignment' AND NOT peer.retired LIMIT ?
			) AS probe) AS members
			FROM (SELECT DISTINCT topic_id FROM links) selected CROSS JOIN scope
		), ranked AS (
			SELECT links.topic_id,row_number() OVER (PARTITION BY links.evidence_id ORDER BY sizes.members,links.topic_id) AS rank
			FROM links JOIN sizes USING(topic_id) WHERE members BETWEEN 2 AND ?
		)
		SELECT DISTINCT topic_id::text FROM ranked WHERE rank<=? ORDER BY topic_id::text`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(input.EvidenceIDs), ontology.MaxVocabularyCandidates,
		input.MaxMembers+1, input.MaxMembers, ontology.MaxRecallPeerTopics).Scan(&ids).Error
	return ids, err
}
