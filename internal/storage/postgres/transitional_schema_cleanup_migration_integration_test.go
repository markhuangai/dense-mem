//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestTransitionalSchemaCleanupPreservesSemanticOwnersAndInstallsDirectForeignKeys(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, 2026080905)

	teamID, ownerID := insertMigrationTeamProfile(t, ctx, db)
	insertMigrationAuthorityFixture(t, ctx, db, teamID, ownerID, "primary")
	require.NoError(t, migrationUpTo(ctx, db, 2026081501))

	contractID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model, dimensions,
				distance_metric, vector_normalization, document_format_version,
				query_format_version, lifecycle_state
			) VALUES ($1::uuid, $2, 1, 'openai', 'cleanup-model', 3,
				'cosine', 'provider', 1, 1, 'active')
		`, contractID, "cleanup-contract-"+contractID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_config (id, model, dimensions)
			VALUES (1, 'cleanup-model', 3)
			ON CONFLICT (id) DO UPDATE
			SET model = EXCLUDED.model, dimensions = EXCLUDED.dimensions
		`)
		return err
	}))

	require.NoError(t, migrationUpTo(ctx, db, 2026081602))
	for _, table := range []string{"semantic_team_refs", "semantic_profile_refs", "embedding_config"} {
		require.False(t, tableExists(t, ctx, db, table), "%s should be removed", table)
	}

	ownerTables := []string{
		"dream_cycle_runs", "embedding_jobs", "entity_correction_events", "entity_correction_plans",
		"entity_names", "entity_resolution_events", "evidence_fragments", "evidence_lifecycle_operations",
		"evidence_quarantines", "evidence_security_events", "evidence_security_signals",
		"evidence_source_revisions", "evidence_sources", "hypotheses", "hypothesis_feedback_events",
		"knowledge_ingests", "placement_items", "placement_outcomes", "placement_runs",
		"relationship_conflict_derived_evidence_tasks", "relationship_conflict_events",
		"relationship_conflict_evidence_derivations", "relationship_correction_submissions",
		"relationship_cross_references", "relationship_evidence_supports", "relationship_observations",
		"relationship_records", "relationship_support_decision_events", "relationship_transition_events",
		"review_tasks", "search_documents", "submission_holds", "verification_events",
	}
	teamTables := []string{
		"community_snapshot_runs", "entity_records", "search_projection_generations",
		"team_predicate_definitions", "value_records",
	}
	require.Equal(t, 37, countValidatedRestrictForeignKeys(t, ctx, db, ownerTables, "ownership_aliases"))
	require.Equal(t, 5, countValidatedRestrictForeignKeys(t, ctx, db, teamTables, "teams"))

	var temporaryConstraints int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_constraint WHERE conname LIKE 'dense_mem_v25_%'
	`).Scan(&temporaryConstraints))
	require.Zero(t, temporaryConstraints)

	var fragments int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM evidence_fragments
		WHERE team_id = $1::uuid AND owner_profile_id = $2::uuid
	`, teamID, ownerID).Scan(&fragments))
	require.Equal(t, 1, fragments)

	otherTeamID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name) VALUES ($1::uuid, $2)`, otherTeamID, "other-team-"+otherTeamID)
		return err
	}))
	err := execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (team_id, owner_profile_id)
			VALUES ($1::uuid, $2::uuid)
		`, otherTeamID, ownerID)
		return execErr
	})
	var postgresError *pgconn.PgError
	require.ErrorAs(t, err, &postgresError)
	require.Equal(t, "23503", postgresError.Code)

	var retentionIndex string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT pg_get_indexdef(indexrelid)
		FROM pg_index
		WHERE indexrelid = 'embedding_jobs_terminal_retention_idx'::regclass
		  AND indisvalid
	`).Scan(&retentionIndex))
	require.Contains(t, retentionIndex, "completed_at, team_id, embedding_job_id")
	require.Contains(t, retentionIndex, "status = ANY (ARRAY['completed'::text, 'stale'::text, 'cancelled'::text])")
}

func TestTransitionalSchemaCleanupEmbeddingMismatchRollsBackOnlyDestructiveMigration(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, 2026081501)

	contractID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model, dimensions,
				distance_metric, vector_normalization, document_format_version,
				query_format_version, lifecycle_state
			) VALUES ($1::uuid, $2, 1, 'openai', 'canonical-model', 3,
				'cosine', 'provider', 1, 1, 'active')
		`, contractID, "mismatch-contract-"+contractID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_config (id, model, dimensions)
			VALUES (1, 'legacy-model', 3)
		`)
		return err
	}))

	err := migrationUpTo(ctx, db, 2026081602)
	require.ErrorContains(t, err, "embedding_config does not match")
	require.True(t, tableExists(t, ctx, db, "semantic_team_refs"))
	require.True(t, tableExists(t, ctx, db, "semantic_profile_refs"))
	require.True(t, tableExists(t, ctx, db, "embedding_config"))
	require.True(t, indexExists(t, ctx, db, "embedding_jobs_terminal_retention_idx"))

	var applied bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM goose_db_version
			WHERE version_id = 2026081602 AND is_applied
		)
	`).Scan(&applied))
	require.False(t, applied)
}

func countValidatedRestrictForeignKeys(t *testing.T, ctx context.Context, db *sql.DB, sourceTables []string, targetTable string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint AS constraint_state
		WHERE constraint_state.contype = 'f'
		  AND constraint_state.conrelid::regclass::text = ANY($1::text[])
		  AND constraint_state.confrelid = $2::regclass
		  AND constraint_state.convalidated
		  AND constraint_state.confdeltype = 'r'
	`, sourceTables, targetTable).Scan(&count))
	return count
}
