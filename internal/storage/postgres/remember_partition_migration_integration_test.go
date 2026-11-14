//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRememberPartitionMigrationValidationAllowsConcurrentAccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	require.NoError(t, evidenceOnlyMigrationUpTo(ctx, db, 20261113020000))
	constraints := []string{"evidence_lifecycle_events_replacement_check", "evidence_lifecycle_replacement_item_fk"}
	validated := func(name string) bool {
		t.Helper()
		var value bool
		require.NoError(t, db.QueryRowContext(ctx, "SELECT convalidated FROM pg_constraint WHERE conrelid='evidence_lifecycle_events'::regclass AND conname=$1", name).Scan(&value))
		return value
	}
	for _, name := range constraints {
		require.False(t, validated(name), "historical validation must follow the committed metadata migration: %s", name)
	}
	writer, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer writer.Rollback()
	_, err = writer.ExecContext(ctx, "LOCK TABLE evidence_lifecycle_events IN ROW EXCLUSIVE MODE")
	require.NoError(t, err)
	validationCtx, cancelValidation := context.WithTimeout(ctx, 10*time.Second)
	defer cancelValidation()
	require.NoError(t, evidenceOnlyMigrationUpTo(validationCtx, db, 20261114010000), "validation must permit concurrent DML locks")
	for _, name := range constraints {
		require.True(t, validated(name), name)
	}
	require.NoError(t, writer.Rollback())
}
