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
		SELECT event.replacement_fragment_id::text, event.target_fragment_id::text
		FROM evidence_lifecycle_events AS event
		WHERE event.team_id = ?::uuid
		  AND event.replacement_fragment_id = ANY(?::uuid[])
		  AND `+activeSemanticSpaceGenerationSQL("event")+`
		ORDER BY replacement_fragment_id ASC, target_fragment_id ASC
	`, teamID, pqStringArray(ids)).Rows()
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
