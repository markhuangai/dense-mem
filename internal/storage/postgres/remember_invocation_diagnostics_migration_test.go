package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberInvocationDiagnosticsMigrationDefinesAppendOnlyBoundedRLSStore(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "postgres", "v2_6", "20260919010001_remember_invocation_diagnostics.sql")
	migration, err := os.ReadFile(path)
	require.NoError(t, err)
	sql := string(migration)
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS remember_invocation_diagnostics",
		"remember_invocation_diagnostics_body_size_check",
		"request_capture_state TEXT NOT NULL DEFAULT 'not_captured'",
		"response_capture_state TEXT NOT NULL DEFAULT 'not_captured'",
		"remember_invocation_diagnostics_capture_state_check",
		"remember_invocation_diagnostics_capture_reason_check",
		"remember_invocation_diagnostics_expiry_check",
		"duration_ms BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE remember_invocation_diagnostics ENABLE ROW LEVEL SECURITY",
		"CREATE POLICY remember_invocation_diagnostics_select",
		"CREATE POLICY remember_invocation_diagnostics_insert",
		"CREATE POLICY remember_invocation_diagnostics_update",
		"CREATE POLICY remember_invocation_diagnostics_delete",
		"CREATE TRIGGER remember_invocation_diagnostics_append_only",
		"RAISE EXCEPTION 'remember invocation diagnostics migration is irreversible",
	} {
		require.Contains(t, sql, fragment)
	}
	require.Equal(t, 1, strings.Count(sql, "CREATE TABLE IF NOT EXISTS remember_invocation_diagnostics"))
}
