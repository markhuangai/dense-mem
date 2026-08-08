package repository

import (
	"context"
	"strconv"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func ensureCommunitySourcesCurrent(ctx context.Context, tx *gorm.DB, teamID string, sources []CommunitySourceInput) error {
	if len(sources) == 0 {
		return nil
	}
	relationshipIDs := make([]string, 0, len(sources))
	versions := make([]int, 0, len(sources))
	for _, source := range sources {
		relationshipIDs = append(relationshipIDs, source.RelationshipID)
		versions = append(versions, source.RelationshipVersion)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH expected AS (
			SELECT *
			FROM unnest(?::uuid[], ?::int[]) AS e(relationship_id, relationship_version)
		), latest_support AS (
			SELECT DISTINCT ON (team_id, support_id) team_id, support_id, decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			ORDER BY team_id, support_id, created_at DESC, support_decision_id DESC
		)
		SELECT expected.relationship_id::text
		FROM expected
		LEFT JOIN relationship_records AS relationship
		  ON relationship.team_id = ?::uuid
		 AND relationship.relationship_id = expected.relationship_id
		WHERE relationship.relationship_id IS NULL
		   OR relationship.version <> expected.relationship_version
		   OR relationship.status <> 'active'
		   OR (relationship.object_entity_id IS NULL AND relationship.object_value_id IS NULL)
			OR NOT EXISTS (
				SELECT 1
				FROM relationship_evidence_supports AS support
				JOIN latest_support AS decision
				  ON decision.team_id = support.team_id
				 AND decision.support_id = support.support_id
				 AND decision.decision IN ('grant', 'reinstate')
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
				  AND support.relationship_id = expected.relationship_id
				  AND quarantine.quarantine_id IS NULL
				  AND lifecycle.lifecycle_event_id IS NULL
				  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
			)
		LIMIT 1
	`, pq.Array(relationshipIDs), pq.Array(versions), teamID, teamID, teamID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrCommunitySourceStale
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func flattenCommunitySources(communities []CommunityPublishRecord) []CommunitySourceInput {
	seen := map[string]struct{}{}
	out := make([]CommunitySourceInput, 0)
	for _, community := range communities {
		for _, source := range community.Sources {
			key := source.RelationshipID + "\x00" + strconv.Itoa(source.RelationshipVersion)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, source)
		}
	}
	return out
}
