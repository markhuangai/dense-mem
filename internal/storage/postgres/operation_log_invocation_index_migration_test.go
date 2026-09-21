package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperationLogInvocationIndexMigrationContract(t *testing.T) {
	migrationFile, err := migrationPath(getMigrationsDir(), 20260921010001)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	for _, required := range []string{
		"-- +goose NO TRANSACTION",
		"SET lock_timeout = '30s';",
		"operation_logs_team_invocation_timestamp_idx_invalid",
		"state.indisvalid IS FALSE",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS operation_logs_team_invocation_timestamp_idx",
		"ON operation_logs(",
		"(attrs ->> 'invocation_id') IS NOT NULL",
		"Lock/rewrite impact",
		"WAL/disk",
		"RLS impact",
		"Backfill",
		"Backward compatibility",
		"Recovery",
		"Rollback",
		"RESET lock_timeout;",
		"-- +goose Down",
	} {
		require.Contains(t, migration, required)
	}
	require.Equal(t, 1, strings.Count(migration, "CREATE INDEX CONCURRENTLY IF NOT EXISTS operation_logs_team_invocation_timestamp_idx\n"))
	require.Equal(t, 1, strings.Count(migration, "DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx;\n"))
}
