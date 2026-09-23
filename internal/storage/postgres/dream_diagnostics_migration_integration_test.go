//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDreamDiagnosticsMigrationRetriesAndRejectsRollback(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	require.NoError(t, runtimeMigrationUpTo(ctx, db, operationLogInvocationIndexMigrationVersion))
	require.NoError(t, runtimeMigrationUpTo(ctx, db, 20260921010005))
	_, err := db.ExecContext(ctx, `
		EXPLAIN INSERT INTO dream_path_evaluations (
			team_id, space_id, space_generation, first_relationship_id, first_relationship_version,
			second_relationship_id, second_relationship_version, allowed_predicate_fingerprint, provider_model
		) VALUES (
			'00000000-0000-0000-0000-000000000001'::uuid,
			'00000000-0000-0000-0000-000000000002'::uuid,
			1,
			'00000000-0000-0000-0000-000000000003'::uuid,
			1,
			'00000000-0000-0000-0000-000000000004'::uuid,
			1,
			repeat('a', 64),
			'legacy-writer'
		)
		ON CONFLICT (
			team_id, first_relationship_id, first_relationship_version,
			second_relationship_id, second_relationship_version,
			allowed_predicate_fingerprint
		) DO NOTHING
	`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id = 20260921010005")
	require.NoError(t, err)
	require.NoError(t, runtimeMigrationUpTo(ctx, db, 20260921010005))

	err = runtimeMigrationDownTo(ctx, db, operationLogInvocationIndexMigrationVersion)
	require.ErrorContains(t, err, "irreversible")
}
