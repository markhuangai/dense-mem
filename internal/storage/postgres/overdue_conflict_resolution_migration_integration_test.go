//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverdueConflictResolutionMigrationAvoidsLegacySystemProfileNameCollision(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026073002)
	teamID, userProfileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	legacyName := "__dense_mem_conflict_system__:" + teamID
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE team_profiles
			SET name = $1
			WHERE team_id = $2::uuid
			  AND id = $3::uuid
		`, legacyName, teamID, userProfileID)
		return err
	}))

	runGooseUpTo(t, ctx, sqlDB, 2026073101)

	var systemProfileID, systemName, authSource string
	var isSystem bool
	var semanticRefCount int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT id::text, name, auth_source, is_system
			FROM team_profiles
			WHERE team_id = $1::uuid
			  AND is_system
		`, teamID).Scan(&systemProfileID, &systemName, &authSource, &isSystem); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM semantic_profile_refs
			WHERE team_id = $1::uuid
			  AND profile_id = $2::uuid
		`, teamID, systemProfileID).Scan(&semanticRefCount)
	}))
	assert.NotEqual(t, userProfileID, systemProfileID)
	assert.NotEqual(t, legacyName, systemName)
	assert.True(t, strings.HasPrefix(systemName, "__dense_mem_conflict_system__:"))
	assert.Equal(t, "system", authSource)
	assert.True(t, isSystem)
	assert.Equal(t, 1, semanticRefCount)
}
