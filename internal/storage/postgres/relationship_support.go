package postgres

import (
	"context"
	"gorm.io/gorm"
)

type RelationshipSupportCounts struct {
	SupportCount     int
	SourceGroupCount int
	HasAuthoritative bool
}

func RelationshipSupportCountsTx(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) (RelationshipSupportCounts, error) {
	row := tx.WithContext(ctx).Raw(`
		WITH latest AS (
			SELECT DISTINCT ON (support_id)
			       support_id,
			       decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			ORDER BY support_id, created_at DESC, support_decision_id DESC
		),
		counts AS (
			SELECT COUNT(*)::int AS support_count,
			       COUNT(DISTINCT support.source_group_key)::int AS source_group_count,
			       COALESCE(bool_or(support.authority = 'authoritative'), false) AS has_authoritative
			FROM relationship_evidence_supports AS support
			JOIN latest
			  ON latest.support_id = support.support_id
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
			  AND support.relationship_id = ?::uuid
		  AND latest.decision IN ('grant', 'reinstate')
		  AND quarantine.quarantine_id IS NULL
		  AND lifecycle.lifecycle_event_id IS NULL
			  AND (
			      support.source_id IS NULL
			      OR source.current_revision_id = support.source_revision_id
			  )
		)
		SELECT support_count, source_group_count, has_authoritative
		FROM counts
	`, teamID, relationshipID, teamID, relationshipID).Row()
	var counts RelationshipSupportCounts
	if err := row.Scan(&counts.SupportCount, &counts.SourceGroupCount, &counts.HasAuthoritative); err != nil {
		return RelationshipSupportCounts{}, err
	}
	return counts, nil
}
