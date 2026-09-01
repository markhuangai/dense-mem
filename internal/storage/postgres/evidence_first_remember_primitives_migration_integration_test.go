//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvidenceFirstRememberPrimitivesMigrationEnforcesDurableContract(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, 20260831010001)

	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	legacyFailedID := "00000000-0000-4000-8000-000000000701"
	legacyCompletedID := "00000000-0000-4000-8000-000000000702"
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, row := range []struct {
			attemptID, key, outcome string
		}{
			{legacyFailedID, "legacy-null-failed", "failed"},
			{legacyCompletedID, "legacy-null-completed", "completed"},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO remember_attempts (
				    team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				    contract_version, submission_kind, outcome, public_result, completed_at
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, 'legacy-test', 'remember', $6, '{}'::jsonb, now())
			`, teamID, row.attemptID, profileID, row.key, row.key+"-hash", row.outcome); err != nil {
				return err
			}
		}
		return nil
	}))
	legacyHeldSpaceID, legacyFreeSpaceID := uuid.NewString(), uuid.NewString()
	legacyHeldAttemptID, legacyFreeAttemptID := uuid.NewString(), uuid.NewString()
	legacyHeldArtifactID, legacyFreeArtifactID := uuid.NewString(), uuid.NewString()
	legacyCapturedAt := "now() - interval '8 days'"
	legacyExpiresAt := "now() - interval '1 day'"
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, space := range []struct {
			id string
		}{
			{legacyHeldSpaceID},
			{legacyFreeSpaceID},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO memory_spaces (id, team_id, kind, owner_credential_id, generation, lifecycle_state)
				VALUES ($1::uuid, $2::uuid, 'credential_private', gen_random_uuid(), 1, 'active')
			`, space.id, teamID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO private_memory_legal_holds (id, team_id, space_id, reason_code, actor_class, placed_at)
			VALUES (gen_random_uuid(), $1::uuid, $2::uuid, 'migration_hold', 'control', now() - interval '8 days')
		`, teamID, legacyHeldSpaceID); err != nil {
			return err
		}
		for _, row := range []struct {
			attemptID, artifactID, key, spaceID string
		}{
			{legacyHeldAttemptID, legacyHeldArtifactID, "legacy-held-artifact", legacyHeldSpaceID},
			{legacyFreeAttemptID, legacyFreeArtifactID, "legacy-free-artifact", legacyFreeSpaceID},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO remember_attempts (
				    team_id, attempt_id, owner_profile_id, space_id, space_generation,
				    idempotency_key, request_hash, contract_version, submission_kind,
				    outcome, failed_phase, error_code, public_result, completed_at
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, $5, $6,
				          'legacy-test', 'remember', 'failed', 'assessment', 'provider_unavailable', '{}'::jsonb, now() - interval '8 days')
			`, teamID, row.attemptID, profileID, row.spaceID, row.key, row.key+"-hash"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO remember_failure_artifacts (
				    team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
				    content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'failure', 'application/json',
					          convert_to('{"legacy":true}', 'UTF8'), octet_length(convert_to('{"legacy":true}', 'UTF8')), 'sha256:600bfa81b1561fa6281505a8630327ec94da208976f36c142c781b0b46a95725', `+legacyCapturedAt+`, `+legacyExpiresAt+`)
			`, teamID, row.artifactID, row.attemptID, profileID); err != nil {
				return err
			}
		}
		return nil
	}))
	runGooseUpTo(t, ctx, db, 20260901010001)

	var nullable string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'remember_attempts' AND column_name = 'retryable'
	`).Scan(&nullable))
	assert.Equal(t, "YES", nullable)
	assert.True(t, columnExists(t, ctx, db, "remember_failure_artifacts", "retained_by_legal_hold"))
	assert.True(t, constraintExists(t, ctx, db, "remember_attempts_retryable_outcome_check"))
	assert.True(t, constraintExists(t, ctx, db, "remember_failure_artifacts_retention_size_check"))
	assert.True(t, indexExists(t, ctx, db, "remember_attempts_failed_retryable_idx"))

	var policyCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_policies
		WHERE schemaname = 'public' AND tablename = 'remember_failure_artifacts'
		  AND policyname = 'remember_failure_artifacts_update'
	`).Scan(&policyCount))
	assert.Equal(t, 1, policyCount)

	var failedEffective, completedEffective bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COALESCE(retryable, outcome = 'failed')
		FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, legacyFailedID).Scan(&failedEffective))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COALESCE(retryable, outcome = 'failed')
		FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, legacyCompletedID).Scan(&completedEffective))
	assert.True(t, failedEffective)
	assert.False(t, completedEffective)
	var heldRetained, freeRetained bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT retained_by_legal_hold
		FROM remember_failure_artifacts
		WHERE team_id = $1::uuid AND artifact_id = $2::uuid
	`, teamID, legacyHeldArtifactID).Scan(&heldRetained))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT retained_by_legal_hold
		FROM remember_failure_artifacts
		WHERE team_id = $1::uuid AND artifact_id = $2::uuid
	`, teamID, legacyFreeArtifactID).Scan(&freeRetained))
	assert.True(t, heldRetained)
	assert.False(t, freeRetained)

	err := execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
			    team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
			    contract_version, submission_kind, outcome, retryable, public_result, completed_at
			) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, 'invalid-retryability', 'invalid-retryability-hash',
			         'migration-test', 'remember', 'completed', true, '{}'::jsonb, now())
		`, teamID, profileID)
		return err
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remember_attempts_retryable_outcome_check")

	err = migrationDownTo(ctx, db, 20260831010001)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evidence-first Remember primitives are append-only")
	assert.True(t, columnExists(t, ctx, db, "remember_attempts", "retryable"))
}
