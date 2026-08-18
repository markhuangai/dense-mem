package repository

import (
	"context"

	"gorm.io/gorm"
)

func hydrateEvidenceLifecycleLineage(ctx context.Context, tx *gorm.DB, teamID string, evidence []EvidenceFragment) error {
	if len(evidence) == 0 {
		return nil
	}
	ids := make([]string, 0, len(evidence))
	byID := make(map[string]*EvidenceFragment, len(evidence))
	for i := range evidence {
		ids = append(ids, evidence[i].FragmentID)
		byID[evidence[i].FragmentID] = &evidence[i]
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT replacement_fragment_id::text, target_fragment_id::text
		FROM evidence_lifecycle_events
		WHERE team_id = ?::uuid
		  AND replacement_fragment_id = ANY(?::uuid[])
		ORDER BY replacement_fragment_id ASC, target_fragment_id ASC
	`, teamID, postgresStringArray(ids)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var replacementID, targetID string
		if err := rows.Scan(&replacementID, &targetID); err != nil {
			return err
		}
		if fragment := byID[replacementID]; fragment != nil {
			fragment.SupersededEvidenceIDs = append(fragment.SupersededEvidenceIDs, targetID)
		}
	}
	return rows.Err()
}
