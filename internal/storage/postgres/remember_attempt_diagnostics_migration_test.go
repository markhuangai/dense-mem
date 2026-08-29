package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberAttemptDiagnosticsIndexesMigrationContract(t *testing.T) {
	const version int64 = 20260829020001
	migrationFile, err := migrationPath(getMigrationsDir(), version)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	for _, required := range []string{
		"-- +goose NO TRANSACTION",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_diagnostics_created_idx",
		"ON remember_attempts(team_id, created_at DESC, attempt_id DESC)",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_diagnostics_outcome_created_idx",
		"ON remember_attempts(team_id, outcome, created_at DESC, attempt_id DESC)",
		"remember_attempts_diagnostics_created_idx_invalid",
		"remember_attempts_diagnostics_outcome_created_idx_invalid",
		"RLS impact",
		"Backward compatibility",
		"Rollback",
	} {
		require.Contains(t, migration, required)
	}
	require.Equal(t, 1, strings.Count(migration, "CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_diagnostics_created_idx\n"))
	require.Equal(t, 1, strings.Count(migration, "CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_diagnostics_outcome_created_idx\n"))
	require.Contains(t, migration, "-- +goose Down")
}
