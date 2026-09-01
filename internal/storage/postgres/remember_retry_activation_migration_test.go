package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberRetryActivationMigrationContainsForwardOnlyIndexRecovery(t *testing.T) {
	const version int64 = 20260901020001
	migrationFile, err := migrationPath(getMigrationsDir(), version)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	for _, required := range []string{
		"-- +goose NO TRANSACTION",
		"SET lock_timeout = '30s';",
		"remember_attempts_failed_retryable_idx_invalid",
		"state.indisvalid IS FALSE",
		"DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_failed_retryable_idx",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_failed_retryable_idx",
		"WHERE outcome = 'failed' AND COALESCE(retryable, true)",
		"Remember retry activation is append-only",
	} {
		require.Contains(t, migration, required)
	}
	require.NotContains(t, migration, "set_config('lock_timeout', '30s', true)")
	require.Equal(t, 1, strings.Count(migration, "CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_failed_retryable_idx\n"))
}
