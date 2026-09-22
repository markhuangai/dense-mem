//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDreamDiagnosticsMigrationRetriesAndRejectsRollback(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	require.NoError(t, runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion))
	_, err := db.ExecContext(ctx, `
		CREATE TABLE dream_diagnostic_constraint_collision (
			id INTEGER PRIMARY KEY,
			CONSTRAINT dream_path_evaluations_run_fk CHECK (id > 0)
		)
	`)
	require.NoError(t, err)
	require.NoError(t, runtimeMigrationUpTo(ctx, db, 20260921010005))
	var foreignKeyExists bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'dream_path_evaluations'::regclass
			  AND conname = 'dream_path_evaluations_run_fk'
		)
	`).Scan(&foreignKeyExists)
	require.NoError(t, err)
	require.True(t, foreignKeyExists)

	_, err = db.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id = 20260921010005")
	require.NoError(t, err)
	require.NoError(t, runtimeMigrationUpTo(ctx, db, 20260921010005))

	err = runtimeMigrationDownTo(ctx, db, operationLogInvocationIndexMigrationVersion)
	require.ErrorContains(t, err, "irreversible")
}
