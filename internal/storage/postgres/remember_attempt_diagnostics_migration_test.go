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

func TestRememberAttemptDiagnosticsMigrationContract(t *testing.T) {
	migrationFile, err := migrationPath(getMigrationsDir(), 20260908010001)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS remember_attempt_diagnostics",
		"remember_attempt_diagnostics_kind_check",
		"remember_attempt_diagnostics_body_size_check",
		"remember_attempt_diagnostics_expiry_check",
		"CREATE POLICY remember_attempt_diagnostics_select",
		"CREATE POLICY remember_attempt_diagnostics_insert",
		"CREATE POLICY remember_attempt_diagnostics_update",
		"CREATE POLICY remember_attempt_diagnostics_delete",
		"private_memory_legal_holds",
		"digest(artifact.content_bytes, 'sha256')",
		"DROP TABLE IF EXISTS remember_failure_artifacts",
	} {
		require.Contains(t, migration, required)
	}
	require.Equal(t, 1, strings.Count(migration, "CREATE TABLE IF NOT EXISTS remember_attempt_diagnostics"))
}
