//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func duplicateTeamSharedSpace(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID string) (string, int64) {
	t.Helper()
	var spaceID string
	var generation int64
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text, generation FROM memory_spaces WHERE team_id = ?::uuid AND kind = 'team_shared' LIMIT 1`, teamID).Row().Scan(&spaceID, &generation)
	}))
	return spaceID, generation
}
