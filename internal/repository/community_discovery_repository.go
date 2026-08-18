package repository

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/postgrescompat"
	"gorm.io/gorm"
)

func (r *SemanticRepositoryImpl) RecallCommunityDiscovery(ctx context.Context, input CommunityDiscoveryInput) ([]CommunityDiscoveryPath, error) {
	input = normalizeCommunityDiscoveryInput(input)
	if err := validateCommunityDiscoveryInput(input); err != nil {
		return nil, err
	}
	if input.Query == "" && len(input.ExpandFromEntityIDs) == 0 {
		return []CommunityDiscoveryPath{}, nil
	}
	paths := []CommunityDiscoveryPath{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH matched_communities AS (
				SELECT record.team_id, record.community_id,
				       row_number() OVER (ORDER BY record.member_count DESC, record.community_id ASC)::int AS community_rank
				FROM community_records AS record
				WHERE record.team_id = ?::uuid
				  AND record.status = 'current'
				  AND (
				      (
				          ? <> ''
				          AND community_record_search_vector(record.summary, record.top_entities, record.top_predicates) @@ plainto_tsquery('simple', ?)
				      )
				      OR (
				          cardinality(?::uuid[]) > 0
				          AND EXISTS (
				              SELECT 1
				              FROM community_memberships AS membership
				              WHERE membership.team_id = record.team_id
				                AND membership.community_id = record.community_id
				                AND membership.entity_id = ANY(?::uuid[])
				          )
				      )
				  )
				ORDER BY record.member_count DESC, record.community_id ASC
				LIMIT ?
			),
			latest_support_decision AS (
				SELECT DISTINCT ON (team_id, support_id)
				       team_id, support_id, decision
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid
				ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
			),
			canonical_names AS (
				SELECT DISTINCT ON (team_id, entity_id)
				       team_id, entity_id, display_name
				FROM entity_names
				WHERE team_id = ?::uuid
				  AND name_kind = 'canonical'
				  AND valid_to IS NULL
				ORDER BY team_id, entity_id, created_at DESC, entity_name_id DESC
			)
			SELECT community.community_id::text,
			       community.community_rank,
			       community_source.source_rank,
			       relationship.relationship_id::text,
			       relationship.subject_entity_id::text,
			       COALESCE(subject_name.display_name, relationship.subject_entity_id::text) AS subject_name,
			       relationship.predicate_key,
			       relationship.object_entity_id::text,
			       COALESCE(object_name.display_name, relationship.object_entity_id::text) AS object_name,
			       relationship.polarity,
			       array_agg(DISTINCT support.fragment_id::text ORDER BY support.fragment_id::text) AS evidence_ids
			FROM matched_communities AS community
			JOIN community_sources AS community_source
			  ON community_source.team_id = community.team_id
			 AND community_source.community_id = community.community_id
			JOIN relationship_records AS relationship
			  ON relationship.team_id = community_source.team_id
			 AND relationship.relationship_id = community_source.relationship_id
			 AND relationship.version = community_source.relationship_version
			 AND relationship.status = 'active'
			 AND relationship.support_count > 0
			 AND relationship.object_entity_id IS NOT NULL
			LEFT JOIN canonical_names AS subject_name
			  ON subject_name.team_id = relationship.team_id
			 AND subject_name.entity_id = relationship.subject_entity_id
			LEFT JOIN canonical_names AS object_name
			  ON object_name.team_id = relationship.team_id
			 AND object_name.entity_id = relationship.object_entity_id
			JOIN relationship_evidence_supports AS support
			  ON support.team_id = relationship.team_id
			 AND support.relationship_id = relationship.relationship_id
			JOIN latest_support_decision AS latest
			  ON latest.team_id = support.team_id
			 AND latest.support_id = support.support_id
			 AND latest.decision IN ('grant', 'reinstate')
			LEFT JOIN evidence_quarantines AS quarantine
			  ON quarantine.team_id = support.team_id
			 AND quarantine.fragment_id = support.fragment_id
			 AND quarantine.status = 'active'
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = support.team_id
			 AND source.source_id = support.source_id
			LEFT JOIN evidence_lifecycle_events AS lifecycle
			  ON lifecycle.team_id = support.team_id
			 AND lifecycle.target_fragment_id = support.fragment_id
			WHERE community.team_id = ?::uuid
			  AND quarantine.quarantine_id IS NULL
			  AND lifecycle.lifecycle_event_id IS NULL
			  AND (
			      support.source_id IS NULL
			      OR source.current_revision_id = support.source_revision_id
			  )
			  AND (
			      ?::timestamptz IS NULL
			      OR ((relationship.valid_from IS NULL OR relationship.valid_from <= ?::timestamptz)
			          AND (relationship.valid_to IS NULL OR relationship.valid_to > ?::timestamptz))
			  )
			  AND (
			      ?::timestamptz IS NULL
			      OR (relationship.created_at <= ?::timestamptz
			          AND support.created_at <= ?::timestamptz
			          AND (relationship.recorded_to IS NULL OR relationship.recorded_to > ?::timestamptz))
			  )
			  AND (
			      cardinality(?::uuid[]) = 0
			      OR relationship.relationship_id <> ALL(?::uuid[])
			  )
			GROUP BY community.community_id, community.community_rank, community_source.source_rank,
			         relationship.relationship_id, relationship.subject_entity_id, subject_name.display_name,
			         relationship.predicate_key, relationship.object_entity_id, object_name.display_name,
			         relationship.polarity
			ORDER BY community.community_rank ASC, community_source.source_rank ASC, relationship.relationship_id ASC
			LIMIT ?
		`, input.TeamID, input.Query, input.Query,
			postgrescompat.Array(input.ExpandFromEntityIDs), postgrescompat.Array(input.ExpandFromEntityIDs),
			input.Limit, input.TeamID, input.TeamID, input.TeamID,
			input.ValidAt, input.ValidAt, input.ValidAt,
			input.KnownAt, input.KnownAt, input.KnownAt, input.KnownAt,
			postgrescompat.Array(input.KnownRelationshipIDs), postgrescompat.Array(input.KnownRelationshipIDs),
			input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			path := CommunityDiscoveryPath{}
			var evidenceIDs postgrescompat.StringArray
			if err := rows.Scan(
				&path.CommunityID,
				&path.CommunityRank,
				&path.SourceRank,
				&path.Relationship.RelationshipID,
				&path.Relationship.SubjectEntityID,
				&path.Relationship.SubjectName,
				&path.Relationship.PredicateKey,
				&path.Relationship.ObjectEntityID,
				&path.Relationship.ObjectName,
				&path.Relationship.Polarity,
				&evidenceIDs,
			); err != nil {
				return err
			}
			path.EvidenceIDs = []string(evidenceIDs)
			paths = append(paths, path)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: recall discovery: %w", err)
	}
	return paths, nil
}
