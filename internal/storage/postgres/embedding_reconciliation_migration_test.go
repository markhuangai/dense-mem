package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddingReconciliationMigrationHasOneRestartGatedBackfill(t *testing.T) {
	migrationFile, err := migrationPath(getMigrationsDir(), 2026080905)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	require.Contains(t, migration, "requires a coordinated application restart")
	require.Equal(t, 1, strings.Count(migration, "CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080905()"))
	require.Contains(t, migration, "FOR UPDATE")
	require.NotContains(t, migration, "SKIP LOCKED")
	require.GreaterOrEqual(t, strings.Count(migration, "LIMIT 1000"), 3)
	require.GreaterOrEqual(t, strings.Count(migration, "COMMIT;"), 3)
	require.Equal(t, 3, strings.Count(migration, "LOOP\n        PERFORM set_config('app.tx_mode', 'migration', true);"))
	require.NotContains(t, migration, "EXIT WHEN updated_rows = 0;\n        PERFORM set_config")
	require.NotContains(t, migration, "embedding_failure_incidents")
	require.NotContains(t, migration, "CREATE TRIGGER")
	require.NotContains(t, migration, "pg_advisory")
	require.Contains(t, migration, "embedding_jobs_failure_groups_idx")
	require.Contains(t, migration, "INCLUDE (first_failed_at, last_failed_at)")
}
