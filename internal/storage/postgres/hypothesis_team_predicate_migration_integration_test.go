//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHypothesisTeamPredicateMigrationScopesForeignKeyByTeam(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026080305)
	teamA, profileA := insertMigrationTeamProfile(t, ctx, sqlDB)
	teamB, profileB := insertMigrationTeamProfile(t, ctx, sqlDB)
	insertHypothesisMigrationSemanticRefs(t, ctx, sqlDB, teamA, profileA)
	insertHypothesisMigrationSemanticRefs(t, ctx, sqlDB, teamB, profileB)
	insertHypothesisMigrationTeamPredicate(t, ctx, sqlDB, teamA, "depends_transitively_on", 1)

	err := insertHypothesisMigrationRow(ctx, sqlDB, teamA, "depends_transitively_on", 1)
	require.Error(t, err, "the legacy global predicate foreign key must reproduce the production failure")
	assertPostgresConstraintViolation(t, err, "hypotheses_predicate_fk")

	require.NoError(t, insertHypothesisMigrationRow(ctx, sqlDB, teamB, "uses", 1))
	require.NoError(t, goose.SetDialect("postgres"))
	err = goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026080501)
	require.ErrorContains(t, err, "1 hypothesis rows lack a matching team predicate definition")

	insertHypothesisMigrationTeamPredicate(t, ctx, sqlDB, teamB, "uses", 1)
	require.NoError(t, goose.UpToContext(ctx, sqlDB, getMigrationsDir(), 2026080501))

	definition := postgresConstraintDefinition(t, ctx, sqlDB, "hypotheses_team_predicate_fk")
	assert.Contains(t, definition, "FOREIGN KEY (team_id, predicate_key, predicate_version)")
	assert.Contains(t, definition, "REFERENCES team_predicate_definitions(team_id, predicate_key, version)")
	assert.False(t, postgresConstraintExists(t, ctx, sqlDB, "hypotheses_predicate_fk"))

	require.NoError(t, insertHypothesisMigrationRow(ctx, sqlDB, teamA, "depends_transitively_on", 1))
	err = insertHypothesisMigrationRow(ctx, sqlDB, teamB, "depends_transitively_on", 1)
	require.Error(t, err, "a predicate registered for one team must not authorize another team")
	assertPostgresConstraintViolation(t, err, "hypotheses_team_predicate_fk")

	err = goose.DownToContext(ctx, sqlDB, getMigrationsDir(), 2026080305)
	require.ErrorContains(t, err, "1 hypothesis rows use team-only predicates")
	assert.True(t, postgresConstraintExists(t, ctx, sqlDB, "hypotheses_team_predicate_fk"))
}

func TestHypothesisTeamPredicateMigrationRollsBackBeforeTeamOnlyHypothesesExist(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026080501)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.DownToContext(ctx, sqlDB, getMigrationsDir(), 2026080305))

	definition := postgresConstraintDefinition(t, ctx, sqlDB, "hypotheses_predicate_fk")
	assert.Contains(t, definition, "FOREIGN KEY (predicate_key, predicate_version)")
	assert.Contains(t, definition, "REFERENCES predicate_definitions(predicate_key, version)")
	assert.False(t, postgresConstraintExists(t, ctx, sqlDB, "hypotheses_team_predicate_fk"))
}

func insertHypothesisMigrationSemanticRefs(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	teamID string,
	profileID string,
) {
	t.Helper()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_team_refs (team_id)
			VALUES ($1::uuid)
		`, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_profile_refs (team_id, profile_id)
			VALUES ($1::uuid, $2::uuid)
		`, teamID, profileID)
		return err
	}))
}

func insertHypothesisMigrationTeamPredicate(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	teamID string,
	predicateKey string,
	version int,
) {
	t.Helper()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if predicateKey == "uses" {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO team_predicate_definitions (
					team_id, predicate_key, version, aliases, allowed_subject_kinds,
					allowed_object_kinds, relationship_kind, current_cardinality,
					lifecycle_state, origin, metadata, created_at
				)
				SELECT $1::uuid, predicate_key, version, aliases, allowed_subject_kinds,
				       allowed_object_kinds, relationship_kind, current_cardinality,
				       lifecycle_state, 'built_in', metadata, created_at
				FROM predicate_definitions
				WHERE predicate_key = $2
				  AND version = $3
			`, teamID, predicateKey, version)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, aliases, allowed_subject_kinds,
				allowed_object_kinds, relationship_kind, current_cardinality,
				lifecycle_state, origin
			) VALUES (
				$1::uuid, $2, $3, ARRAY[]::text[], ARRAY['project']::text[],
				ARRAY['product']::text[], 'state', 'many', 'active', 'provider_generated'
			)
		`, teamID, predicateKey, version)
		return err
	}))
}

func insertHypothesisMigrationRow(
	ctx context.Context,
	db *sql.DB,
	teamID string,
	predicateKey string,
	version int,
) error {
	return execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hypotheses (team_id, predicate_key, predicate_version)
			VALUES ($1::uuid, $2, $3)
		`, teamID, predicateKey, version)
		return err
	})
}

func assertPostgresConstraintViolation(t *testing.T, err error, constraintName string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "23503", pgErr.Code)
	assert.Equal(t, constraintName, pgErr.ConstraintName)
}

func postgresConstraintDefinition(t *testing.T, ctx context.Context, db *sql.DB, constraintName string) string {
	t.Helper()
	var definition string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'hypotheses'::regclass
		  AND conname = $1
	`, constraintName).Scan(&definition))
	return definition
}

func postgresConstraintExists(t *testing.T, ctx context.Context, db *sql.DB, constraintName string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'hypotheses'::regclass
			  AND conname = $1
		)
	`, constraintName).Scan(&exists))
	return exists
}
