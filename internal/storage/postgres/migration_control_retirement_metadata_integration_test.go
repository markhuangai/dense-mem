//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationControlRetirementRejectsMissingApprovedCommit(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, sqlDB)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE v2_migration_operator_actions
			   SET metadata = '{
				 "detached_release_receipt": "fixture://detached-release",
				 "current_main_rehearsal": "fixture://current-main",
				 "backup_restore_rehearsal": "fixture://backup-restore",
				 "coordinated_stop": "fixture://coordinated-stop",
				 "node_fence": "fixture://coordinated-stop",
				 "catalog_preflight": "fixture://catalog"
			   }'::jsonb
			 WHERE action = 'retire_migration_control'
		`)
		return err
	}))

	err := migrationUpTo(ctx, sqlDB, migrationControlRetirementVersion)
	require.Error(t, err)
	require.Contains(t, err.Error(), "operator authorization is incomplete")
	require.False(t, migrationControlRetirementApplied(t, ctx, sqlDB))
	for _, table := range migrationControlRetirementTables {
		require.True(t, tableExists(t, ctx, sqlDB, table), "%s must remain when approved_commit is missing", table)
	}
}
