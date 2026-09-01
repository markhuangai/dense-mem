package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvidenceFirstRememberPrimitivesMigrationContainsContract(t *testing.T) {
	const version int64 = 20260901010001
	migrationFile, err := migrationPath(getMigrationsDir(), version)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	for _, required := range []string{
		"-- +goose NO TRANSACTION",
		"ADD COLUMN IF NOT EXISTS retryable BOOLEAN NULL",
		"remember_attempts_retryable_outcome_check",
		"remember_attempts_failed_retryable_idx",
		"remember_attempts_failed_retryable_idx_invalid",
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_failed_retryable_idx",
		"ADD COLUMN IF NOT EXISTS retained_by_legal_hold BOOLEAN NOT NULL DEFAULT false",
		"remember_failure_artifacts_retention_size_check",
		"CREATE POLICY remember_failure_artifacts_update ON remember_failure_artifacts",
		"evidence-first Remember primitives are append-only after deployment",
	} {
		require.Contains(t, migration, required)
	}
	require.Equal(t, 1, strings.Count(migration, "ADD COLUMN IF NOT EXISTS retryable BOOLEAN NULL"))
	require.Equal(t, 1, strings.Count(migration, "ADD COLUMN IF NOT EXISTS retained_by_legal_hold BOOLEAN NOT NULL DEFAULT false"))
	require.Equal(t, 1, strings.Count(migration, "CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_failed_retryable_idx\n"))
}
