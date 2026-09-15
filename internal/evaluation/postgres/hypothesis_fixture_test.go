//go:build integration

package postgres

import (
	"context"

	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func createHypothesisForTest(ctx context.Context, db *gorm.DB, rls storagepostgres.RLSHelper, teamID, ownerID, text string) (string, error) {
	var hypothesisID string
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			INSERT INTO hypotheses (team_id, created_by_profile_id, status, payload)
			VALUES (?::uuid, ?::uuid, 'proposed', jsonb_build_object('text', ?::text))
			RETURNING hypothesis_id::text
		`, teamID, ownerID, text).Row().Scan(&hypothesisID)
	})
	return hypothesisID, err
}
