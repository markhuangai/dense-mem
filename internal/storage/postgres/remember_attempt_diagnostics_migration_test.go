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
		"capture_state TEXT NOT NULL DEFAULT 'captured'",
		"remember_attempt_diagnostics_capture_state_check",
		"'not_delivered'",
		"'hash_only'",
		"'legacy_quarantine_omitted'",
		"legacy_hash_summary",
		"remember_attempt_diagnostics_body_size_check",
		"remember_attempt_diagnostics_expiry_check",
		"CREATE POLICY remember_attempt_diagnostics_select",
		"CREATE POLICY remember_attempt_diagnostics_insert",
		"CREATE POLICY remember_attempt_diagnostics_update",
		"CREATE POLICY remember_attempt_diagnostics_delete",
		"SELECT set_config('app.tx_mode', 'migration', true)",
		"CREATE OR REPLACE FUNCTION prevent_remember_attempt_diagnostics_mutation()",
		"CREATE TRIGGER remember_attempt_diagnostics_append_only",
		"to_jsonb(NEW) - ARRAY['retained_by_legal_hold']",
		"OLD.expires_at <= clock_timestamp()",
		"private_memory_legal_holds",
		"digest(artifact.content_bytes, 'sha256')",
		"previous-version replicas can still",
		"TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'remember_failure_artifacts'",
		"remember_failure_artifacts_team_id_attempt_id_owner_profil_fkey",
		"ON DELETE CASCADE",
		"NOT VALID",
		"set_config('lock_timeout', '30s', true)",
		"pg_trigger_depth() > 1",
		"RAISE EXCEPTION 'remember attempt diagnostics migration is irreversible",
	} {
		require.Contains(t, migration, required)
	}
	require.Equal(t, 1, strings.Count(migration, "CREATE TABLE IF NOT EXISTS remember_attempt_diagnostics"))
	require.NotContains(t, migration, "DROP TABLE IF EXISTS remember_failure_artifacts")
}

func TestRememberAttemptDiagnosticsUnavailableCaptureMigrationContract(t *testing.T) {
	migrationFile, err := migrationPath(getMigrationsDir(), 20260919010002)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)
	require.Contains(t, migration, "DROP CONSTRAINT IF EXISTS remember_attempt_diagnostics_capture_state_check")
	require.Contains(t, migration, "'unavailable'")
	require.Contains(t, migration, "NOT VALID")
	require.Contains(t, migration, "VALIDATE CONSTRAINT remember_attempt_diagnostics_capture_state_check")
	require.Contains(t, migration, "cannot remove unavailable capture state while diagnostics exist")
	require.Contains(t, migration, "LOCK TABLE remember_attempt_diagnostics IN ACCESS EXCLUSIVE MODE")
	require.Contains(t, migration, "BEGIN;\nSET LOCAL lock_timeout = '30s';\nALTER TABLE remember_attempt_diagnostics")
	require.Contains(t, migration, "SET LOCAL lock_timeout = '30s';")
	require.Equal(t, 3, strings.Count(migration, "RESET lock_timeout;"))
}
