//go:build integration

package postgres

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

func insertEvidenceQuarantine(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID, fragmentID, reason string) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_quarantines (
			team_id, fragment_id, ingest_id, owner_profile_id, reason,
			space_id, space_generation
		)
		SELECT ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, fragment.space_id, fragment.space_generation
		FROM evidence_fragments AS fragment
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id = ?::uuid
		  AND fragment.ingest_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
		ON CONFLICT (team_id, fragment_id) DO NOTHING
	`, input.TeamID, fragmentID, ingestID, input.OwnerProfileID, strings.TrimSpace(reason), input.TeamID, fragmentID, ingestID, input.OwnerProfileID).Error
}
