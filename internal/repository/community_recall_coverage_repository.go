package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/postgrescompat"
	"gorm.io/gorm"
)

// ListCommunitySemanticGroups resolves only current, team-scoped community
// source groups. It is used to suppress known or already-returned context
// before community hydration, without exposing cross-team IDs.
func (r *SemanticRepositoryImpl) ListCommunitySemanticGroups(ctx context.Context, input CommunityCoverageInput) ([]string, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	input.EvidenceIDs = normalizeCommunityIDs(input.EvidenceIDs)
	input.RelationshipIDs = normalizeCommunityIDs(input.RelationshipIDs)
	if len(input.EvidenceIDs) == 0 && len(input.RelationshipIDs) == 0 {
		return []string{}, nil
	}
	groups := []string{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH latest_support AS (
				SELECT DISTINCT ON (team_id, support_id)
				       team_id, support_id, decision
				FROM relationship_support_decision_events
				WHERE team_id = ?::uuid
				ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
			), eligible_support AS (
				SELECT DISTINCT support.relationship_id
				FROM relationship_evidence_supports AS support
				JOIN latest_support AS latest
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
				WHERE support.team_id = ?::uuid
				  AND lifecycle.lifecycle_event_id IS NULL
				  AND quarantine.quarantine_id IS NULL
				  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
				  AND (
					  support.relationship_id = ANY(?::uuid[])
					  OR support.fragment_id = ANY(?::uuid[])
				  )
			)
			SELECT DISTINCT source.semantic_group_key
			FROM community_records AS record
			JOIN community_sources AS source
			  ON source.team_id = record.team_id
			 AND source.community_id = record.community_id
			JOIN relationship_records AS relationship
			  ON relationship.team_id = source.team_id
			 AND relationship.relationship_id = source.relationship_id
			 AND relationship.version = source.relationship_version
			JOIN eligible_support AS support
			  ON support.relationship_id = relationship.relationship_id
			WHERE record.team_id = ?::uuid
			  AND record.status = 'current'
			  AND relationship.status = 'active'
			  AND relationship.identity_alias_of_relationship_id IS NULL
			  AND source.semantic_group_key <> ''
			ORDER BY source.semantic_group_key
		`, input.TeamID, input.TeamID, postgrescompat.Array(input.RelationshipIDs), postgrescompat.Array(input.EvidenceIDs), input.TeamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var group string
			if err := rows.Scan(&group); err != nil {
				return err
			}
			groups = append(groups, group)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: resolve covered groups: %w", err)
	}
	return groups, nil
}

var _ interface {
	ListCommunitySemanticGroups(context.Context, CommunityCoverageInput) ([]string, error)
} = (*SemanticRepositoryImpl)(nil)
