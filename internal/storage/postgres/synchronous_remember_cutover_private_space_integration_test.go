//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRememberSynchronousCutoverPreservesPrivateAttemptLineageForErasure(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	spaceID, ingestID, runID, relationshipID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	eventID, assessmentID := uuid.New(), uuid.New()
	const generation int64 = 7

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memory_spaces (id, team_id, kind, owner_profile_id, generation)
			VALUES ($1::uuid, $2::uuid, 'profile_private', $3::uuid, $4)
		`, spaceID, teamID, profileID, generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
			          'private-space-cutover', 'private-space-cutover-request', 'completed',
			          '{"relationship_hints":[{"ref":"private-erasure-fixture"}]}'::jsonb,
			          '{"_dense_mem_telemetry_origin":"remember"}'::jsonb, now())
		`, teamID, ingestID, profileID, spaceID, generation); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				status, attempts, max_attempts, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'completed', 1, 5, now())
		`, teamID, runID, ingestID, profileID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO submission_relationship_results (
				team_id, ingest_id, placement_run_id, owner_profile_id,
				relationship_ref, disposition, reason, splits, space_id, space_generation
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'private-erasure-fixture', 'stored', '',
			          jsonb_build_array(jsonb_build_object(
			              'split_index', 0,
			              'relationship_id', $5::text,
			              'relationship_version', 1,
			              'status', 'active')),
			          $6::uuid, $7)
		`, teamID, ingestID, runID, profileID, relationshipID, spaceID, generation)
		return err
	}))

	runGooseUpTo(t, ctx, db, synchronousRememberCutoverMigrationVersion)

	var migratedSpaceID string
	var migratedGeneration int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT space_id::text, space_generation
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&migratedSpaceID, &migratedGeneration))
	require.Equal(t, spaceID.String(), migratedSpaceID)
	require.Equal(t, generation, migratedGeneration)
	var relationshipDisposition string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT public_result -> 'relationship_results' -> 0 ->> 'disposition'
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&relationshipDisposition))
	require.Equal(t, "stored", relationshipDisposition)

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempt_events (
				team_id, event_id, attempt_id, owner_profile_id,
				sequence_no, phase, event_kind, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          99, 'private_erasure_fixture', 'private_erasure_fixture', 'recorded')
		`, teamID, eventID, ingestID, profileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_assessments (
				team_id, semantic_assessment_id, attempt_id, owner_profile_id,
				response_history, provider_turns, model
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          '[]'::jsonb, 1, 'private-erasure-fixture')
		`, teamID, assessmentID, ingestID, profileID)
		return err
	}))

	var attemptCount, eventCount, assessmentCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&attemptCount))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_attempt_events
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid AND event_id = $3::uuid
	`, teamID, ingestID, eventID).Scan(&eventCount))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM semantic_assessments
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid AND semantic_assessment_id = $3::uuid
	`, teamID, ingestID, assessmentID).Scan(&assessmentCount))
	require.Equal(t, 1, attemptCount)
	require.Equal(t, 1, eventCount)
	require.Equal(t, 1, assessmentCount)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	for key, value := range map[string]string{
		"app.tx_mode":                  "system",
		"app.private_erasure_space_id": spaceID.String(),
	} {
		_, err = tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, key, value)
		require.NoError(t, err)
	}
	for _, statement := range []string{
		`DELETE FROM submission_relationship_results WHERE team_id = $1::uuid AND ingest_id = $2::uuid`,
		`DELETE FROM remember_attempt_events WHERE team_id = $1::uuid AND attempt_id = $2::uuid`,
		`DELETE FROM semantic_assessments WHERE team_id = $1::uuid AND attempt_id = $2::uuid`,
		`DELETE FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`,
		`DELETE FROM knowledge_ingests WHERE team_id = $1::uuid AND ingest_id = $2::uuid`,
	} {
		_, err = tx.ExecContext(ctx, statement, teamID, ingestID)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())

	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&attemptCount))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_attempt_events
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&eventCount))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM semantic_assessments
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&assessmentCount))
	require.Zero(t, attemptCount)
	require.Zero(t, eventCount)
	require.Zero(t, assessmentCount)
}
