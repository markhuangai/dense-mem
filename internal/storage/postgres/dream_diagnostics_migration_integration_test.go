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

	require.NoError(t, runtimeMigrationUpTo(ctx, db, 20260921010005))
	_, err := db.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id = 20260921010005")
	require.NoError(t, err)
	require.NoError(t, runtimeMigrationUpTo(ctx, db, 20260921010005))

	err = runtimeMigrationDownTo(ctx, db, operationLogInvocationIndexMigrationVersion)
	require.ErrorContains(t, err, "irreversible")
}
