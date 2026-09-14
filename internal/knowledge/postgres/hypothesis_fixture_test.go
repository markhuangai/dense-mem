package postgres

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func createHypothesisForTest(ctx context.Context, db *gorm.DB, rls storagepostgres.RLSHelper, teamID, ownerID string, payload map[string]any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var hypothesisID string
	err = rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			INSERT INTO hypotheses (team_id, created_by_profile_id, status, payload)
			VALUES (?::uuid, ?::uuid, 'proposed', ?::jsonb)
			RETURNING hypothesis_id::text
		`, teamID, ownerID, string(encoded)).Row().Scan(&hypothesisID)
	})
	return hypothesisID, err
}
