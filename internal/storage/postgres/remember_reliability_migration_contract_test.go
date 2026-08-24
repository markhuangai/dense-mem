package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRememberReliabilityMigrationContainsFinalV26Contract(t *testing.T) {
	const finalRememberReliabilityMigrationVersion int64 = 20260823010001
	migrationFile, err := migrationPath(getMigrationsDir(), finalRememberReliabilityMigrationVersion)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	for _, required := range []string{
		"CREATE POLICY evidence_fragments_remember_source_bind ON evidence_fragments",
		"CREATE OR REPLACE FUNCTION prevent_evidence_fragment_mutation()",
		"FOR EACH ROW EXECUTE FUNCTION prevent_evidence_fragment_mutation();",
		"RETURN result_reason IN ('not_supported_by_evidence', 'stale_input', 'security_quarantine')",
		"DROP POLICY IF EXISTS evidence_fragments_remember_source_bind ON evidence_fragments;",
		"FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();",
	} {
		require.Contains(t, migration, required)
	}
	require.Equal(t, 1, strings.Count(migration, "CREATE POLICY evidence_fragments_remember_source_bind ON evidence_fragments"))
	require.Equal(t, 1, strings.Count(migration, "CREATE OR REPLACE FUNCTION prevent_evidence_fragment_mutation()"))

	_, err = migrationPath(getMigrationsDir(), 20260823010002)
	require.Error(t, err, "the final v2.6 Remember contract must not depend on a follow-up migration")
}
