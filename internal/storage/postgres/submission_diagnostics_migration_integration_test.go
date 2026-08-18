//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubmissionDiagnosticsIndexesAreValid(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)
	migrator, err := NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))

	for _, indexName := range []string{
		"placement_runs_control_created_idx",
		"placement_runs_control_team_created_idx",
		"operation_logs_event_timestamp_idx",
		"operation_logs_reference_timestamp_idx",
	} {
		var valid bool
		err := db.Raw(`
			SELECT state.indisvalid
			FROM pg_index AS state
			JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
			JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
			WHERE namespace.nspname = 'public' AND index_class.relname = ?
		`, indexName).Row().Scan(&valid)
		require.NoError(t, err, indexName)
		require.True(t, valid, indexName)
	}
}
