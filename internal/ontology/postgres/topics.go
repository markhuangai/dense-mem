package postgres

import (
	"context"
	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
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
		heads, loadErr := loadHeads(tx, fence, input.TopicIDs, nil, nil)
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
	} else if len(input.EvidenceIDs) > 0 {
		records, err = recallRecords(tx, fence, `head.kind='topic' AND head.record_id=ANY(ARRAY(
            SELECT (revision.body->'assignment'->>'definition_id')::uuid
            FROM ontology_source_dependencies AS source_dependency
            JOIN ontology_record_heads AS assignment
              ON assignment.team_id=source_dependency.team_id AND assignment.shared_space_id=source_dependency.shared_space_id
              AND assignment.space_generation=source_dependency.space_generation AND assignment.record_id=source_dependency.record_id
              AND assignment.version=source_dependency.record_version AND assignment.kind='assignment' AND NOT assignment.retired
            JOIN ontology_revision_records AS revision
              ON revision.team_id=assignment.team_id AND revision.shared_space_id=assignment.shared_space_id
              AND revision.space_generation=assignment.space_generation AND revision.record_id=assignment.record_id AND revision.version=assignment.version
            WHERE source_dependency.team_id=head.team_id AND source_dependency.shared_space_id=head.shared_space_id
              AND source_dependency.space_generation=head.space_generation AND source_dependency.source_kind='evidence'
              AND source_dependency.source_id=ANY(?::text[])))`, []any{pq.Array(input.EvidenceIDs)}, ontology.MaxPageSize)
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
	assignments, err := recallRecords(tx, fence, `head.kind='assignment' AND revision.body->'assignment'->>'definition_id'=ANY(?::text[])
        AND revision.body->'assignment'->'source'->>'kind'='evidence'`, []any{pq.Array(ids)}, ontology.MaxDependencyRecords)
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
