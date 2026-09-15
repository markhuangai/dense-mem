//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func privateSpaceGeneration(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS, spaceID uuid.UUID) int64 {
	t.Helper()
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, spaceID).Row().Scan(&generation)
	}))
	return generation
}
