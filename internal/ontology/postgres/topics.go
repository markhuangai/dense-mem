package postgres

import (
	"context"
	"sort"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) NewTopicCatalogReader() func(context.Context, *gorm.DB, ontology.TopicCatalogCursor) (ontology.TopicCatalogPage, error) {
	return s.readTopicCatalog
}

func (s *Store) readTopicCatalog(ctx context.Context, tx *gorm.DB, cursor ontology.TopicCatalogCursor) (ontology.TopicCatalogPage, error) {
	result := ontology.TopicCatalogPage{}
	if err := requireAutomatic(ctx); err != nil {
		return result, err
	}
	rows, err := tx.WithContext(ctx).Raw(`SELECT head.team_id::text,head.shared_space_id::text,
		head.space_generation,head.record_id::text,head.version
		FROM ontology_record_heads AS head
		JOIN memory_spaces AS space ON space.team_id=head.team_id AND space.id=head.shared_space_id
		AND space.generation=head.space_generation AND space.kind='team_shared' AND space.lifecycle_state='active'
		JOIN teams AS team ON team.id=head.team_id AND team.status='active' AND team.deleted_at IS NULL
		WHERE head.kind='topic' AND NOT head.retired
		AND (head.team_id,head.record_id)>(COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),
		COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid))
		ORDER BY head.team_id,head.record_id LIMIT ?`, cursor.TeamID, cursor.TopicID, ontology.TopicProjectionPageSize+1).Rows()
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry ontology.TopicCatalogEntry
		if err := rows.Scan(&entry.TeamID, &entry.SpaceID, &entry.Generation, &entry.TopicID, &entry.Version); err != nil {
			return result, err
		}
		result.Topics = append(result.Topics, entry)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Topics) > ontology.TopicProjectionPageSize {
		result.More = true
		result.Topics = result.Topics[:ontology.TopicProjectionPageSize]
	}
	if len(result.Topics) > 0 {
		last := result.Topics[len(result.Topics)-1]
		result.Next = ontology.TopicCatalogCursor{TeamID: last.TeamID, TopicID: last.TopicID}
	}
	return result, nil
}

func (s *Store) NewTopicMembershipReader() func(context.Context, *gorm.DB, string, string, ontology.TopicMembershipCursor) (ontology.TopicMembershipPage, error) {
	return s.readTopicMembership
}

func (s *Store) readTopicMembership(ctx context.Context, tx *gorm.DB, teamID, topicID string, cursor ontology.TopicMembershipCursor) (ontology.TopicMembershipPage, error) {
	result := ontology.TopicMembershipPage{}
	fence, err := sharedScope(ctx, tx, teamID)
	if err != nil {
		return result, err
	}
	heads, err := loadHeads(tx, fence, []string{topicID}, nil, nil)
	if err != nil {
		return result, err
	}
	topic, exists := heads[topicID]
	if !exists || topic.Kind != ontology.Topic {
		return result, ontology.ErrNotFound
	}
	views, err := s.currentViews(tx, fence, []ontology.Record{topic})
	if err != nil {
		return result, err
	}
	result.Topic = views[0]
	if !result.Topic.Current {
		catalog, err := validationCatalog(tx, fence, []ontology.Record{topic}, nil)
		if err != nil {
			return result, err
		}
		collectTopicDependencies(&result, catalog)
		return result, nil
	}

	rows, err := tx.WithContext(ctx).Raw(topicMembershipSQL, fence.TeamID, fence.SpaceID,
		fence.Generation, topicID, cursor.AssignmentID, cursor.RelationshipID, ontology.TopicProjectionPageSize+1).Rows()
	if err != nil {
		return result, err
	}
	defer rows.Close()
	type membership struct {
		assignment   ontology.Record
		relationship string
	}
	items := []membership{}
	for rows.Next() {
		var body []byte
		var relationship string
		if err := rows.Scan(&body, &relationship); err != nil {
			return result, err
		}
		record, err := decodeRecord(body)
		if err != nil {
			return result, err
		}
		items = append(items, membership{record, relationship})
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if len(items) > ontology.TopicProjectionPageSize {
		result.More = true
		items = items[:ontology.TopicProjectionPageSize]
	}
	records := []ontology.Record{topic}
	seen := map[string]bool{topic.ID: true}
	for _, item := range items {
		if !seen[item.assignment.ID] {
			records = append(records, item.assignment)
			seen[item.assignment.ID] = true
		}
	}
	views, err = s.currentViews(tx, fence, records)
	if err != nil {
		return result, err
	}
	current := map[string]bool{}
	for _, view := range views {
		current[view.ID] = view.Current
	}
	result.Topic = views[0]
	if !result.Topic.Current {
		return result, ontology.ErrSourceStale
	}
	catalog, err := validationCatalog(tx, fence, records, nil)
	if err != nil {
		return result, err
	}
	collectTopicDependencies(&result, catalog)
	for _, item := range items {
		result.Next = ontology.TopicMembershipCursor{AssignmentID: item.assignment.ID, RelationshipID: item.relationship}
		if current[item.assignment.ID] && item.relationship != "" {
			result.RelationshipIDs = append(result.RelationshipIDs, item.relationship)
		}
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].ID < result.Records[j].ID })
	return result, nil
}

func collectTopicDependencies(result *ontology.TopicMembershipPage, catalog map[string]ontology.Record) {
	seenSources := map[ontology.SourceHandle]bool{}
	for _, record := range catalog {
		result.Records = append(result.Records, record)
		for _, handle := range ontology.RequiredSources(record) {
			if !seenSources[handle] {
				seenSources[handle] = true
				result.Sources = append(result.Sources, handle)
			}
		}
		for _, dependency := range record.Sources {
			if !seenSources[dependency.SourceHandle] {
				seenSources[dependency.SourceHandle] = true
				result.Sources = append(result.Sources, dependency.SourceHandle)
			}
		}
	}
}

const topicMembershipSQL = `WITH assignments AS (
	SELECT head.record_id,revision.body,head.team_id,head.shared_space_id AS space_id,head.space_generation
	FROM ontology_record_heads AS head ` + recallRevisionJoinSQL + `
	WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
	AND head.kind='assignment' AND NOT head.retired AND revision.body->'assignment'->>'definition_id'=?
), memberships AS (
	SELECT assignment.record_id,assignment.body,COALESCE(relationship.relationship_id::text,'') AS relationship_id
	FROM assignments AS assignment LEFT JOIN LATERAL (
		SELECT candidate.relationship_id FROM relationship_records AS candidate
		WHERE candidate.team_id=assignment.team_id AND candidate.space_id=assignment.space_id
		AND candidate.space_generation=assignment.space_generation AND candidate.status='active'
		AND candidate.identity_alias_of_relationship_id IS NULL
		AND ((assignment.body->'assignment'->'source'->>'kind'='relationship'
			AND candidate.relationship_id=(assignment.body->'assignment'->'source'->>'id')::uuid
			AND EXISTS (` + eligibleSupportsSQL + `))
		OR (assignment.body->'assignment'->'source'->>'kind'='evidence'
			AND EXISTS (SELECT 1 FROM (` + eligibleSupportsSQL + `) AS support
			WHERE support.fragment_id=(assignment.body->'assignment'->'source'->>'id')::uuid)))
	) AS relationship ON TRUE
)
SELECT body,relationship_id FROM memberships
WHERE (record_id,COALESCE(NULLIF(relationship_id,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid))>
	(COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),
	COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid))
ORDER BY record_id,relationship_id LIMIT ?`
