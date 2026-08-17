package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func (r *SemanticRepositoryImpl) RecallCommunities(ctx context.Context, input CommunityRecallInput) ([]CommunityRecallRecord, error) {
	input = normalizeCommunityRecallInput(input)
	if err := validateCommunityRecallInput(input); err != nil {
		return nil, err
	}
	communities := []CommunityRecallRecord{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH params AS (
				SELECT ?::uuid AS team_id,
				       ?::text AS query,
				       ?::uuid[] AS returned_evidence_ids,
				       ?::uuid[] AS known_evidence_ids,
				       ?::uuid[] AS known_relationship_ids,
				       ?::uuid[] AS seed_relationship_ids,
				       ?::uuid[] AS expand_entity_ids,
				       ?::text[] AS covered_groups
			), matched_communities AS (
				SELECT record.community_id,
				       record.logical_community_id,
				       record.summary,
				       record.member_count,
				       record.source_count,
				       record.top_predicates,
				       CASE
					       WHEN EXISTS (
						       SELECT 1
						       FROM community_sources source
						       JOIN relationship_evidence_supports support
						         ON support.team_id = source.team_id
						        AND support.relationship_id = source.relationship_id
						       CROSS JOIN params
						       WHERE source.team_id = record.team_id
						         AND source.community_id = record.community_id
						         AND support.fragment_id = ANY(params.returned_evidence_ids)
					       ) THEN 0
					       WHEN EXISTS (
						       SELECT 1
						       FROM community_sources source
						       CROSS JOIN params
						       WHERE source.team_id = record.team_id
						         AND source.community_id = record.community_id
						         AND source.relationship_id = ANY(params.known_relationship_ids)
					       ) OR EXISTS (
						       SELECT 1
						       FROM community_sources source
						       JOIN relationship_evidence_supports support
						         ON support.team_id = source.team_id
						        AND support.relationship_id = source.relationship_id
						       CROSS JOIN params
						       WHERE source.team_id = record.team_id
						         AND source.community_id = record.community_id
						         AND support.fragment_id = ANY(params.known_evidence_ids)
						       ) THEN 1
					       WHEN EXISTS (
						       SELECT 1
						       FROM community_sources source
						       CROSS JOIN params
						       WHERE source.team_id = record.team_id
						         AND source.community_id = record.community_id
						         AND source.relationship_id = ANY(params.seed_relationship_ids)
					       ) OR EXISTS (
						       SELECT 1
						       FROM community_memberships membership
						       CROSS JOIN params
						       WHERE membership.team_id = record.team_id
						         AND membership.community_id = record.community_id
						         AND membership.entity_id = ANY(params.expand_entity_ids)
						       ) THEN 2
					       ELSE 3
				       END AS seed_lane
				FROM community_records record
				CROSS JOIN params
				WHERE record.team_id = params.team_id
				  AND record.status = 'current'
				  AND (
					  params.query = ''
					  OR community_record_search_vector(record.summary, record.top_entities, record.top_predicates) @@ plainto_tsquery('simple', params.query)
					  OR EXISTS (
						  SELECT 1 FROM community_memberships membership
						  WHERE membership.team_id = record.team_id
						    AND membership.community_id = record.community_id
						    AND membership.entity_id = ANY(params.expand_entity_ids)
					  )
					  OR EXISTS (
						  SELECT 1 FROM community_sources source
						  WHERE source.team_id = record.team_id
						    AND source.community_id = record.community_id
						    AND source.relationship_id = ANY(params.known_relationship_ids || params.seed_relationship_ids)
					  )
					  OR EXISTS (
						  SELECT 1
						  FROM community_sources source
						  JOIN relationship_evidence_supports support
						    ON support.team_id = source.team_id
						   AND support.relationship_id = source.relationship_id
						  WHERE source.team_id = record.team_id
						    AND source.community_id = record.community_id
						    AND support.fragment_id = ANY(params.returned_evidence_ids || params.known_evidence_ids)
					  )
				  )
				  AND (
					  cardinality(params.covered_groups) = 0
					  OR EXISTS (
						  SELECT 1 FROM community_sources source
						  WHERE source.team_id = record.team_id
						    AND source.community_id = record.community_id
						    AND source.semantic_group_key <> ALL(params.covered_groups)
					  )
				  )
			)
			SELECT community_id::text,
			       COALESCE(logical_community_id, community_id)::text,
			       row_number() OVER (ORDER BY seed_lane ASC, member_count DESC, community_id ASC)::int,
			       summary, member_count, source_count, top_predicates
			FROM matched_communities
			ORDER BY seed_lane ASC, member_count DESC, community_id ASC
			LIMIT ?
		`, input.TeamID, input.Query,
			pq.Array(input.ReturnedEvidenceIDs), pq.Array(input.KnownEvidenceIDs),
			pq.Array(input.KnownRelationshipIDs), pq.Array(input.SeedRelationshipIDs),
			pq.Array(input.ExpandFromEntityIDs), pq.Array(input.CoveredGroupKeys), input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		matched := make([]CommunityRecallRecord, 0)
		for rows.Next() {
			record := CommunityRecallRecord{}
			var topPredicates pq.StringArray
			if err := rows.Scan(&record.CommunityID, &record.LogicalCommunityID, &record.Rank, &record.Summary, &record.EntityCount, &record.RelationshipCount, &topPredicates); err != nil {
				return err
			}
			record.TopPredicates = []string(topPredicates)
			matched = append(matched, record)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(matched) > 0 {
			if err := loadCommunityTopEntitiesBatch(ctx, tx, input.TeamID, matched); err != nil {
				return err
			}
			if err := loadCommunityRecallRelationshipsBatch(ctx, tx, input, matched); err != nil {
				return err
			}
		}
		for _, record := range matched {
			if len(record.Relationships) > 0 {
				communities = append(communities, record)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("community: recall communities: %w", err)
	}
	return communities, nil
}

func loadCommunityTopEntitiesBatch(ctx context.Context, tx *gorm.DB, teamID string, records []CommunityRecallRecord) error {
	communityIDs := make([]string, 0, len(records))
	byCommunityID := make(map[string]*CommunityRecallRecord, len(records))
	for index := range records {
		communityIDs = append(communityIDs, records[index].CommunityID)
		byCommunityID[records[index].CommunityID] = &records[index]
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH ranked_memberships AS (
			SELECT membership.community_id::text AS community_id,
			       membership.entity_id::text AS entity_id,
			       COALESCE(name.display_name, membership.entity_id::text) AS entity_name,
			       row_number() OVER (
				       PARTITION BY membership.community_id
				       ORDER BY membership.rank ASC, membership.entity_id ASC
			       ) AS membership_rank
			FROM community_memberships membership
			LEFT JOIN entity_names name
			  ON name.team_id = membership.team_id
			 AND name.entity_id = membership.entity_id
			 AND name.name_kind = 'canonical'
			 AND name.valid_to IS NULL
			WHERE membership.team_id = ?::uuid
			  AND membership.community_id = ANY(?::uuid[])
		)
		SELECT community_id, entity_id, entity_name
		FROM ranked_memberships
		WHERE membership_rank <= 5
		ORDER BY community_id, membership_rank, entity_id
	`, teamID, pq.Array(communityIDs)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var communityID string
		var entity CommunityRecallTopEntity
		if err := rows.Scan(&communityID, &entity.EntityID, &entity.Name); err != nil {
			return err
		}
		if record := byCommunityID[communityID]; record != nil {
			record.TopEntities = append(record.TopEntities, entity)
		}
	}
	return rows.Err()
}

func loadCommunityRecallRelationshipsBatch(ctx context.Context, tx *gorm.DB, input CommunityRecallInput, records []CommunityRecallRecord) error {
	communityIDs := make([]string, 0, len(records))
	byCommunityID := make(map[string]*CommunityRecallRecord, len(records))
	for index := range records {
		communityIDs = append(communityIDs, records[index].CommunityID)
		byCommunityID[records[index].CommunityID] = &records[index]
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest_support AS (
			SELECT DISTINCT ON (team_id, support_id) team_id, support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			  AND (?::timestamptz IS NULL OR created_at <= ?::timestamptz)
			ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
		), effective_support AS (
			SELECT support.relationship_id,
			       array_agg(DISTINCT support.fragment_id::text ORDER BY support.fragment_id::text) AS evidence_ids
			FROM relationship_evidence_supports support
			JOIN latest_support latest ON latest.team_id = support.team_id AND latest.support_id = support.support_id AND latest.decision IN ('grant', 'reinstate')
			LEFT JOIN evidence_quarantines quarantine ON quarantine.team_id = support.team_id AND quarantine.fragment_id = support.fragment_id AND quarantine.status = 'active'
			LEFT JOIN evidence_sources source ON source.team_id = support.team_id AND source.source_id = support.source_id
			LEFT JOIN evidence_lifecycle_events lifecycle ON lifecycle.team_id = support.team_id AND lifecycle.target_fragment_id = support.fragment_id
			WHERE support.team_id = ?::uuid
			  AND quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
			GROUP BY support.relationship_id
		), ranked_relationships AS (
		SELECT community_source.community_id::text AS community_id,
		       relationship.relationship_id::text AS relationship_id,
		       relationship.semantic_group_key AS semantic_group_key,
		       relationship.subject_entity_id::text AS subject_entity_id,
		       COALESCE(subject_name.display_name, relationship.subject_entity_id::text) AS subject_name,
		       relationship.predicate_key AS predicate_key,
		       COALESCE(relationship.object_entity_id::text, '') AS object_entity_id,
		       COALESCE(relationship.object_value_id::text, '') AS object_value_id,
		       COALESCE(object_name.display_name, value_record.display, value_record.canonical_value, '') AS object_name,
		       COALESCE(value_record.value_type, '') AS object_value_type,
		       COALESCE(value_record.canonical_value, '') AS object_value,
		       relationship.polarity AS polarity,
		       effective_support.evidence_ids AS evidence_ids,
		       community_source.source_rank AS source_rank,
		       relationship.created_at AS created_at,
		       row_number() OVER (
			       PARTITION BY community_source.community_id
			       ORDER BY community_source.source_rank ASC, relationship.relationship_id ASC
		       ) AS community_rank
		FROM community_sources community_source
		JOIN relationship_records relationship
		  ON relationship.team_id = community_source.team_id
		 AND relationship.relationship_id = community_source.relationship_id
		 AND relationship.version = community_source.relationship_version
		JOIN effective_support ON effective_support.relationship_id = relationship.relationship_id
		LEFT JOIN entity_names subject_name
		  ON subject_name.team_id = relationship.team_id AND subject_name.entity_id = relationship.subject_entity_id AND subject_name.name_kind = 'canonical' AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names object_name
		  ON object_name.team_id = relationship.team_id AND object_name.entity_id = relationship.object_entity_id AND object_name.name_kind = 'canonical' AND object_name.valid_to IS NULL
		LEFT JOIN value_records value_record
		  ON value_record.team_id = relationship.team_id AND value_record.value_id = relationship.object_value_id
		WHERE community_source.team_id = ?::uuid
		  AND community_source.community_id = ANY(?::uuid[])
		  AND relationship.identity_alias_of_relationship_id IS NULL
		  AND relationship.status = 'active'
		  AND (?::timestamptz IS NULL OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz) AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz)))
		  AND (?::timestamptz IS NULL OR (relationship.created_at <= ?::timestamptz AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz)))
		  AND (cardinality(?::uuid[]) = 0 OR relationship.relationship_id <> ALL(?::uuid[]))
		  AND (cardinality(?::text[]) = 0 OR relationship.semantic_group_key <> ALL(?::text[]))
		)
		SELECT community_id, relationship_id, semantic_group_key, subject_entity_id, subject_name,
		       predicate_key, object_entity_id, object_value_id, object_name, object_value_type,
		       object_value, polarity, evidence_ids, source_rank, created_at
		FROM ranked_relationships
		WHERE community_rank <= ?
		ORDER BY community_id, community_rank, relationship_id
	`, input.TeamID, input.KnownAt, input.KnownAt, input.TeamID,
		input.TeamID, pq.Array(communityIDs), input.ValidAt, input.ValidAt, input.ValidAt,
		input.KnownAt, input.KnownAt, input.KnownAt,
		pq.Array(input.KnownRelationshipIDs), pq.Array(input.KnownRelationshipIDs),
		pq.Array(input.CoveredGroupKeys), pq.Array(input.CoveredGroupKeys), input.RelationshipLimit+1).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var communityID string
		var hit RecallRelationshipHit
		var evidenceIDs pq.StringArray
		var sourceRank int
		if err := rows.Scan(&communityID, &hit.RelationshipID, &hit.SemanticGroupKey, &hit.SubjectEntityID, &hit.SubjectName, &hit.PredicateKey,
			&hit.ObjectEntityID, &hit.ObjectValueID, &hit.ObjectName, &hit.ObjectValueType, &hit.ObjectValue,
			&hit.Polarity, &evidenceIDs, &sourceRank, &hit.CreatedAt); err != nil {
			return err
		}
		hit.TeamID = input.TeamID
		hit.EvidenceIDs = []string(evidenceIDs)
		hit.SearchState = "current"
		if record := byCommunityID[communityID]; record != nil {
			record.Relationships = append(record.Relationships, hit)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range records {
		if len(records[index].Relationships) > input.RelationshipLimit {
			records[index].Relationships = records[index].Relationships[:input.RelationshipLimit]
			records[index].RelationshipsTruncated = true
		}
	}
	return nil
}

func normalizeCommunityRecallInput(input CommunityRecallInput) CommunityRecallInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 {
		input.Limit = 3
	}
	if input.Limit > 10 {
		input.Limit = 10
	}
	if input.RelationshipLimit <= 0 {
		input.RelationshipLimit = 5
	}
	if input.RelationshipLimit > 20 {
		input.RelationshipLimit = 20
	}
	input.KnownEvidenceIDs = normalizeCommunityIDs(input.KnownEvidenceIDs)
	input.KnownRelationshipIDs = normalizeCommunityIDs(input.KnownRelationshipIDs)
	input.ReturnedEvidenceIDs = normalizeCommunityIDs(input.ReturnedEvidenceIDs)
	input.SeedRelationshipIDs = normalizeCommunityIDs(input.SeedRelationshipIDs)
	input.ExpandFromEntityIDs = normalizeCommunityIDs(input.ExpandFromEntityIDs)
	input.ExcludedGroupKeys = normalizeCommunityStrings(input.ExcludedGroupKeys)
	input.CoveredGroupKeys = normalizeCommunityStrings(input.CoveredGroupKeys)
	input.CoveredGroupKeys = appendUniqueCommunityStrings(input.CoveredGroupKeys, input.ExcludedGroupKeys...)
	return input
}

func appendUniqueCommunityStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func validateCommunityRecallInput(input CommunityRecallInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	return nil
}

func normalizeCommunityIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		if _, err := uuid.Parse(value); err != nil {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
func normalizeCommunityStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
