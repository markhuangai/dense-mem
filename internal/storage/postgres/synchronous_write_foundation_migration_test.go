package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSynchronousWriteFoundationMigrationContract(t *testing.T) {
	const version int64 = 20260828010001
	migrationFile, err := migrationPath(getMigrationsDir(), version)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS remember_attempts",
		"CREATE TABLE IF NOT EXISTS remember_attempt_events",
		"CREATE TABLE IF NOT EXISTS remember_failure_artifacts",
		"CREATE TABLE IF NOT EXISTS semantic_assessments",
		"ALTER TABLE relationship_observations",
		"ADD COLUMN IF NOT EXISTS remember_attempt_id UUID NULL",
		"ADD COLUMN IF NOT EXISTS semantic_assessment_id UUID NULL",
		"ADD COLUMN IF NOT EXISTS selected_count BIGINT NOT NULL DEFAULT 0",
		"remember_attempts_replay_shape_check",
		"FOREIGN KEY (team_id, canonical_attempt_id, owner_profile_id)",
		"CREATE OR REPLACE FUNCTION validate_synchronous_write_replay()",
		"remember_attempts_replay_integrity",
		"canonical_attempt.outcome NOT IN ('completed', 'rejected', 'quarantined')",
		"CREATE OR REPLACE FUNCTION validate_synchronous_write_space_generation()",
		"remember_attempts_space_generation",
		"dense_mem_active_space_generation(NEW.team_id, NEW.space_id)",
		"attempt.space_id IS NULL OR dense_mem_space_allowed(attempt.space_id)",
		"table_name IN ('remember_attempt_events', 'remember_failure_artifacts', 'semantic_assessments')",
		"space_id IS NULL OR dense_mem_space_allowed(space_id)",
		"semantic_assessments_revision_check",
		"accepted_revision <= jsonb_array_length(response_history)",
		"validated_at IS NOT NULL",
		"CREATE OR REPLACE FUNCTION prevent_synchronous_write_append_only_mutation()",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"20260828010001 is irreversible after synchronous-write foundation history exists",
	} {
		require.Contains(t, migration, required)
	}
	require.NotContains(t, migration, "INSERT INTO v2_compatibility_markers")
	require.NotContains(t, migration, "UPDATE knowledge_ingests")
	require.NotContains(t, migration, "DELETE FROM")
	up := strings.SplitN(migration, "-- +goose Down", 2)[0]
	require.NotRegexp(t, `(?im)^\s*DROP\s+`, up)
	require.Equal(t, 1, strings.Count(migration, "CREATE TABLE IF NOT EXISTS remember_attempts"))
}
