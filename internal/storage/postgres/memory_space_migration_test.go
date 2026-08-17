//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemorySpacePrivateInitializerCanUpsertUnderForceRLS(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	require.NoError(t, NewMigratorWithDB(sqlDB).RunUp(ctx))
	teamID := uuid.New()
	ownerID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO teams (id, name, description, metadata, config)
			VALUES ($1, $2, '', '{}'::jsonb, '{}'::jsonb)
		`, teamID, "memory-space-policy-test")
		return err
	}))

	var firstID, secondID uuid.UUID
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "team", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true)`, teamID.String()); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT dense_mem_ensure_private_space($1, 'credential_private', $2)`, teamID, ownerID).Scan(&firstID); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT dense_mem_ensure_private_space($1, 'credential_private', $2)`, teamID, ownerID).Scan(&secondID)
	}))
	assert.Equal(t, firstID, secondID, "private-space initialization should be idempotent")
}
