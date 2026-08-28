//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

const synchronousWriteFoundationMigrationVersion int64 = 20260828010001

func TestSynchronousWriteFoundationMigrationCreatesEmptyAppendOnlySurface(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, synchronousWriteFoundationMigrationVersion)
	for _, table := range []string{"remember_attempts", "remember_attempt_events", "remember_failure_artifacts", "semantic_assessments"} {
		require.True(t, tableExists(t, ctx, db, table), table)
		var count int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count, table)
	}
	for _, column := range []string{"selected_count", "embedded_count", "updated_count", "drifted_count"} {
		require.True(t, columnExists(t, ctx, db, "embedding_reconciliation_runs", column), column)
	}
	for _, column := range []string{"remember_attempt_id", "semantic_assessment_id"} {
		require.True(t, columnExists(t, ctx, db, "verification_events", column), column)
	}
	var enabled, forced bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class WHERE oid = 'remember_attempts'::regclass
	`).Scan(&enabled, &forced))
	require.True(t, enabled)
	require.True(t, forced)
}

func TestSynchronousWriteFoundationMigrationPreservesPopulatedUpgrade(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	const baseVersion int64 = 20260823010001
	runGooseUpTo(t, ctx, db, baseVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID := uuid.NewString()
	contractID := uuid.NewString()
	runID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, source_summary, status, proposal, metadata, error
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'populated-upgrade-key',
			          'populated-upgrade-request', 'pre-foundation row', 'completed',
			          '{"kind":"legacy"}'::jsonb, '{"preserve":true}'::jsonb, '')
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model,
				dimensions, distance_metric, vector_normalization,
				document_format_version, query_format_version, lifecycle_state
			) VALUES ($1::uuid, 'populated-upgrade-contract', 1, 'openai',
			          'populated-upgrade-model', 2, 'cosine', 'provider', 1, 1, 'active')
		`, contractID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_reconciliation_runs (
				reconciliation_run_id, embedding_contract_id, embedding_dimensions,
				local_run_date, status, requeued_count, recovered_count, last_error
			) VALUES ($1::uuid, $2::uuid, 2, DATE '2026-08-27', 'failed', 3, 4,
			          'legacy reconciliation failure')
		`, runID, contractID)
		return err
	}))

	var before struct {
		ingestStatus, ingestSummary, ingestProposal, ingestMetadata string
		requeued, recovered                                         int64
		lastError                                                   string
	}
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status, source_summary, proposal::text, metadata::text
		FROM knowledge_ingests WHERE team_id = $1::uuid AND ingest_id = $2::uuid
	`, teamID, ingestID).Scan(
		&before.ingestStatus, &before.ingestSummary, &before.ingestProposal, &before.ingestMetadata,
	))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT requeued_count, recovered_count, last_error
		FROM embedding_reconciliation_runs WHERE reconciliation_run_id = $1::uuid
	`, runID).Scan(&before.requeued, &before.recovered, &before.lastError))

	runGooseUpTo(t, ctx, db, synchronousWriteFoundationMigrationVersion)

	var after struct {
		ingestStatus, ingestSummary, ingestProposal, ingestMetadata string
		requeued, recovered                                         int64
		lastError                                                   string
		selected, embedded, updated, drifted                        int64
	}
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status, source_summary, proposal::text, metadata::text
		FROM knowledge_ingests WHERE team_id = $1::uuid AND ingest_id = $2::uuid
	`, teamID, ingestID).Scan(
		&after.ingestStatus, &after.ingestSummary, &after.ingestProposal, &after.ingestMetadata,
	))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT requeued_count, recovered_count, last_error,
		       selected_count, embedded_count, updated_count, drifted_count
		FROM embedding_reconciliation_runs WHERE reconciliation_run_id = $1::uuid
	`, runID).Scan(
		&after.requeued, &after.recovered, &after.lastError,
		&after.selected, &after.embedded, &after.updated, &after.drifted,
	))

	require.Equal(t, before.ingestStatus, after.ingestStatus)
	require.Equal(t, before.ingestSummary, after.ingestSummary)
	require.Equal(t, before.ingestProposal, after.ingestProposal)
	require.Equal(t, before.ingestMetadata, after.ingestMetadata)
	require.Equal(t, before.requeued, after.requeued)
	require.Equal(t, before.recovered, after.recovered)
	require.Equal(t, before.lastError, after.lastError)
	require.EqualValues(t, 0, after.selected)
	require.EqualValues(t, 0, after.embedded)
	require.EqualValues(t, 0, after.updated)
	require.EqualValues(t, 0, after.drifted)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE embedding_reconciliation_runs
			SET selected_count = 7, embedded_count = 6, updated_count = 5, drifted_count = 1
			WHERE reconciliation_run_id = $1::uuid
		`, runID)
		return err
	}))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT selected_count, embedded_count, updated_count, drifted_count
		FROM embedding_reconciliation_runs WHERE reconciliation_run_id = $1::uuid
	`, runID).Scan(&after.selected, &after.embedded, &after.updated, &after.drifted))
	require.EqualValues(t, 7, after.selected)
	require.EqualValues(t, 6, after.embedded)
	require.EqualValues(t, 5, after.updated)
	require.EqualValues(t, 1, after.drifted)
}

func TestSynchronousWriteFoundationStoresAdditiveLineage(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, synchronousWriteFoundationMigrationVersion)

	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID, placementRunID, entityID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (team_id, ingest_id, owner_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'completed')
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_records (team_id, entity_id, entity_kind)
			VALUES ($1::uuid, $2::uuid, 'project')
		`, teamID, entityID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (team_id, placement_run_id, ingest_id, owner_profile_id, status, completed_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'completed', now())
		`, teamID, placementRunID, ingestID, profileID)
		return err
	}))

	attemptID, assessmentID := uuid.NewString(), uuid.NewString()
	observationID, verificationID, resolutionID, reviewTaskID, registrationID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	responseHash := "sha256:" + strings.Repeat("a", 64)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				contract_version, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'lineage-write', 'lineage-request', 'dense-mem.v2.6', 'completed')
		`, teamID, attemptID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_assessments (
				team_id, semantic_assessment_id, attempt_id, owner_profile_id, response_history,
				accepted_revision, provider_turns, validated_at, response_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, '[{}]'::jsonb, 1, 1, now(), $5)
		`, teamID, assessmentID, attemptID, profileID, responseHash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_observations (
				team_id, observation_id, ingest_id, owner_profile_id,
				subject_ref, original_predicate, object_ref, remember_attempt_id
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
				          'lineage-subject', 'works_on', 'lineage-object', $5::uuid)
		`, teamID, observationID, ingestID, profileID, attemptID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO verification_events (
				team_id, verification_event_id, observation_id, owner_profile_id, evidence_verdict,
				rationale, model, response_hash, remember_attempt_id, semantic_assessment_id
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'entailed',
				          'lineage verification', 'lineage-model', $5, $6::uuid, $7::uuid)
		`, teamID, verificationID, observationID, profileID, responseHash, attemptID, assessmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_resolution_events (
				team_id, resolution_event_id, ingest_id, owner_profile_id, mention_ref, action,
				entity_id, verifier_result, remember_attempt_id, semantic_assessment_id
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'lineage-entity', 'reuse',
				         $5::uuid, '{}'::jsonb, $6::uuid, $7::uuid)
		`, teamID, resolutionID, ingestID, profileID, entityID, attemptID, assessmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_tasks (
				team_id, review_task_id, owner_profile_id, ingest_id, observation_id, task_type,
				status, reason, payload, remember_attempt_id, semantic_assessment_id
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'relationship_needs_review',
				          'open', 'lineage', '{}'::jsonb, $6::uuid, $7::uuid)
		`, teamID, reviewTaskID, profileID, ingestID, observationID, attemptID, assessmentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_assessments (
				team_id, assessment_id, owner_profile_id, request_id, assessor_contract_version,
				model, tokenizer, input_tokens, output_tokens, candidate_context_tokens,
				normalized_response, response_hash, validated_at, assessment_scope, placement_run_id, ingest_id
			) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, 'lineage-placement', 'dense-mem.v2.4',
				          'lineage-model', 'o200k_base', 0, 0, 0, '{}'::jsonb, $3, now(), 'submission', $4::uuid, $5::uuid)
		`, teamID, profileID, responseHash, placementRunID, ingestID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO predicate_registration_events (
				team_id, predicate_registration_event_id, placement_run_id, assessment_id, owner_profile_id,
				relationship_ref, registration_action, predicate_key, predicate_version,
				ingest_id, remember_attempt_id, semantic_assessment_id
			) SELECT $1::uuid, $2::uuid, $3::uuid, assessment_id, $4::uuid,
			         'lineage-relationship', 'reused', 'works_on', 1, $5::uuid, $6::uuid, $7::uuid
			FROM placement_assessments
			WHERE team_id = $1::uuid AND placement_run_id = $3::uuid
			ORDER BY created_at DESC LIMIT 1
		`, teamID, registrationID, placementRunID, profileID, ingestID, attemptID, assessmentID)
		return err
	}))

	for _, check := range []struct {
		name, query, id string
		assessment      bool
	}{
		{"observation", `SELECT remember_attempt_id::text FROM relationship_observations WHERE team_id = $1::uuid AND observation_id = $2::uuid`, observationID, false},
		{"resolution", `SELECT remember_attempt_id::text || ':' || semantic_assessment_id::text FROM entity_resolution_events WHERE team_id = $1::uuid AND resolution_event_id = $2::uuid`, resolutionID, true},
		{"verification", `SELECT remember_attempt_id::text || ':' || semantic_assessment_id::text FROM verification_events WHERE team_id = $1::uuid AND verification_event_id = $2::uuid`, verificationID, true},
		{"review task", `SELECT remember_attempt_id::text || ':' || semantic_assessment_id::text FROM review_tasks WHERE team_id = $1::uuid AND review_task_id = $2::uuid`, reviewTaskID, true},
		{"predicate registration", `SELECT ingest_id::text || ':' || remember_attempt_id::text || ':' || semantic_assessment_id::text FROM predicate_registration_events WHERE team_id = $1::uuid AND predicate_registration_event_id = $2::uuid`, registrationID, true},
	} {
		var got string
		require.NoError(t, db.QueryRowContext(ctx, check.query, teamID, check.id).Scan(&got), check.name)
		require.Contains(t, got, attemptID, check.name)
		if check.assessment {
			require.Contains(t, got, assessmentID, check.name)
		}
	}
}

func TestSynchronousWriteFoundationConstrainsReplayAndAssessmentLineage(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, synchronousWriteFoundationMigrationVersion)

	teamID, profileA := insertRememberReliabilityIdentityFixture(t, ctx, db)
	profileB, identityB := uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO actor_identities (id, kind, team_id, display_name)
			VALUES ($1::uuid, 'human', $2::uuid, 'Synchronous write replay profile B')
		`, identityB, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, reason)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'synchronous_write_replay_test')
		`, teamID, profileB, identityB)
		return err
	}))
	spaceA, spaceB := uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO memory_spaces (id, team_id, kind, owner_credential_id)
			VALUES ($1::uuid, $3::uuid, 'credential_private', $4::uuid),
			       ($2::uuid, $3::uuid, 'credential_private', $5::uuid)
		`, spaceA, spaceB, teamID, uuid.NewString(), uuid.NewString())
		return err
	}))

	canonicalAttemptID, replayAttemptID := uuid.New(), uuid.New()
	insertAttempt := func(attemptID uuid.UUID, ownerID, key, requestHash, submissionKind, outcome string, canonicalID *uuid.UUID) error {
		var canonicalValue any
		if canonicalID != nil {
			canonicalValue = canonicalID.String()
		}
		return execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO remember_attempts (
					team_id, attempt_id, owner_profile_id, idempotency_key,
					request_hash, contract_version, submission_kind, outcome, canonical_attempt_id
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5,
				          'dense-mem.v2.6', $6, $7, $8::uuid)
			`, teamID, attemptID.String(), ownerID, key, requestHash, submissionKind, outcome, canonicalValue)
			return err
		})
	}
	insertSpacedAttempt := func(attemptID uuid.UUID, ownerID, key, requestHash, submissionKind, outcome string, spaceID any, spaceGeneration any, canonicalID *uuid.UUID) error {
		var canonicalValue any
		if canonicalID != nil {
			canonicalValue = canonicalID.String()
		}
		return execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO remember_attempts (
					team_id, attempt_id, owner_profile_id, space_id, space_generation,
					idempotency_key, request_hash, contract_version, submission_kind, outcome, canonical_attempt_id
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::bigint,
				          $6, $7, 'dense-mem.v2.6', $8, $9, $10::uuid)
			`, teamID, attemptID.String(), ownerID, spaceID, spaceGeneration, key, requestHash, submissionKind, outcome, canonicalValue)
			return err
		})
	}
	require.NoError(t, insertAttempt(canonicalAttemptID, profileA, "canonical-lineage", "canonical-lineage-request", "remember", "completed", nil))
	require.NoError(t, insertAttempt(replayAttemptID, profileA, "canonical-lineage", "canonical-lineage-request", "remember", "replayed", &canonicalAttemptID))
	privateCanonicalID, privateReplayID := uuid.New(), uuid.New()
	require.NoError(t, insertSpacedAttempt(privateCanonicalID, profileA, "private-lineage", "private-lineage-request", "remember", "completed", spaceA, int64(1), nil))
	require.NoError(t, insertSpacedAttempt(privateReplayID, profileA, "private-lineage", "private-lineage-request", "remember", "replayed", spaceA, nil, &privateCanonicalID))
	require.Error(t, insertSpacedAttempt(uuid.New(), profileA, "private-lineage", "private-lineage-request", "remember", "replayed", nil, nil, &privateCanonicalID))
	require.Error(t, insertSpacedAttempt(uuid.New(), profileA, "private-lineage", "private-lineage-request", "remember", "replayed", spaceB, int64(1), &privateCanonicalID))

	var linkedCanonicalID string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT canonical_attempt_id::text FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, replayAttemptID.String()).Scan(&linkedCanonicalID))
	require.Equal(t, canonicalAttemptID.String(), linkedCanonicalID)

	require.Error(t, insertAttempt(uuid.New(), profileA, "replay-missing", "replay-missing-request", "remember", "replayed", nil))
	nonexistentCanonicalID := uuid.New()
	require.Error(t, insertAttempt(uuid.New(), profileA, "replay-unknown", "replay-unknown-request", "remember", "replayed", &nonexistentCanonicalID))
	require.Error(t, insertAttempt(uuid.New(), profileB, "canonical-lineage", "canonical-lineage-request", "remember", "replayed", &canonicalAttemptID))
	require.Error(t, insertAttempt(uuid.New(), profileA, "replay-lineage", "replay-lineage-request", "remember", "replayed", &canonicalAttemptID))
	require.Error(t, insertAttempt(uuid.New(), profileA, "canonical-lineage", "different-request", "remember", "replayed", &canonicalAttemptID))
	require.Error(t, insertAttempt(uuid.New(), profileA, "canonical-lineage", "canonical-lineage-request", "relationship_correction", "replayed", &canonicalAttemptID))
	failedRetryID := uuid.New()
	require.NoError(t, insertAttempt(failedRetryID, profileA, "failed-retry", "failed-retry-request", "remember", "failed", nil))
	require.Error(t, insertAttempt(uuid.New(), profileA, "failed-retry", "different-failed-retry-request", "remember", "completed", nil))
	require.NoError(t, insertAttempt(uuid.New(), profileA, "failed-retry", "failed-retry-request", "remember", "completed", nil))
	failedCanonicalID := uuid.New()
	require.NoError(t, insertAttempt(failedCanonicalID, profileA, "failed-canonical", "failed-canonical-request", "remember", "failed", nil))
	require.Error(t, insertAttempt(uuid.New(), profileA, "failed-canonical", "failed-canonical-request", "remember", "replayed", &failedCanonicalID))

	assessmentAttemptID := uuid.New()
	require.NoError(t, insertAttempt(assessmentAttemptID, profileA, "assessment-lineage", "assessment-lineage-request", "remember", "failed", nil))
	insertAssessment := func(assessmentID uuid.UUID, history string, acceptedRevision string, validatedAt string, responseHash string) error {
		return execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO semantic_assessments (
					team_id, semantic_assessment_id, attempt_id, owner_profile_id,
					response_history, accepted_revision, validated_at, response_hash
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::jsonb, $6::integer, `+validatedAt+`, $7)
			`, teamID, assessmentID, assessmentAttemptID, profileA, history, acceptedRevision, responseHash)
			return err
		})
	}
	validResponseHash := "sha256:" + strings.Repeat("1", 64)
	require.Error(t, insertAssessment(uuid.New(), "[]", "1", "NULL", validResponseHash))
	require.Error(t, insertAssessment(uuid.New(), "[{}]", "1", "NULL", validResponseHash))
	require.Error(t, insertAssessment(uuid.New(), "[{}]", "1", "CURRENT_TIMESTAMP", ""))
	require.Error(t, insertAssessment(uuid.New(), "[{}]", "2", "CURRENT_TIMESTAMP", validResponseHash))
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_assessments (
				team_id, semantic_assessment_id, attempt_id, owner_profile_id,
				response_history, accepted_revision, provider_turns, validated_at, response_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, '[{}]'::jsonb, 1, 1, CURRENT_TIMESTAMP, $5)
		`, teamID, uuid.New().String(), assessmentAttemptID.String(), profileA, validResponseHash)
		return err
	}))
}

func TestSynchronousWriteFoundationRLSIsolatesProfileWritesWithinTeam(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, synchronousWriteFoundationMigrationVersion)

	teamID, profileA := insertRememberReliabilityIdentityFixture(t, ctx, db)
	teamC, profileC := insertRememberReliabilityIdentityFixture(t, ctx, db)
	profileB := uuid.NewString()
	identityB := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO actor_identities (id, kind, team_id, display_name)
			VALUES ($1::uuid, 'human', $2::uuid, 'Synchronous write profile B')
		`, identityB, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, reason)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'synchronous_write_rls_test')
		`, teamID, profileB, identityB)
		return err
	}))

	roleName := "dense_mem_synchronous_write_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := db.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("synchronous-write RLS behavior test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() {
		_, _ = db.ExecContext(ctx, "DROP OWNED BY "+quotedRole)
		_, _ = db.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole)
	}()
	_, err := db.ExecContext(ctx, "GRANT USAGE ON SCHEMA public TO "+quotedRole)
	require.NoError(t, err)
	for _, table := range []string{
		"remember_attempts", "remember_attempt_events", "remember_failure_artifacts", "semantic_assessments",
	} {
		_, err = db.ExecContext(ctx, "GRANT SELECT, INSERT, UPDATE, DELETE ON "+table+" TO "+quotedRole)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, "GRANT SELECT ON ownership_aliases TO "+quotedRole)
	require.NoError(t, err)
	spaceA, spaceB := uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO memory_spaces (id, team_id, kind, owner_credential_id)
			VALUES ($1::uuid, $3::uuid, 'credential_private', $4::uuid),
			       ($2::uuid, $3::uuid, 'credential_private', $5::uuid)
		`, spaceA, spaceB, teamID, uuid.NewString(), uuid.NewString())
		return err
	}))

	attemptA, attemptA2, eventA, artifactA, assessmentA := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	// The artifact hash constraint only needs a bounded shape for this RLS test;
	// its content is intentionally a single non-sensitive byte.
	contentHash := "sha256:" + strings.Repeat("0", 64)
	withProfile := func(currentTeamID, profileID string, fn func(*sql.Tx) error) {
		t.Helper()
		tx, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		defer tx.Rollback()
		require.NoError(t, func() error {
			if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
				return err
			}
			for key, value := range map[string]string{
				"app.tx_mode": "profile", "app.current_team_id": currentTeamID, "app.current_profile_id": profileID,
			} {
				if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, key, value); err != nil {
					return err
				}
			}
			return fn(tx)
		}())
		require.NoError(t, tx.Commit())
	}

	withProfile(teamID, profileA, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				contract_version, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'rls-a', 'rls-a-request', 'dense-mem.v2.6', 'failed')
		`, teamID, attemptA, profileA); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
				contract_version, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'rls-a-2', 'rls-a-2-request', 'dense-mem.v2.6', 'failed')
		`, teamID, attemptA2, profileA); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempt_events (
				team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 'commit', 'failed')
		`, teamID, eventA, attemptA, profileA); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_failure_artifacts (
				team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
				content_type, content_bytes, byte_count, content_sha256, expires_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'rls-test',
			          'text/plain', decode('61', 'hex'), 1, $5,
			          now() + interval '1 hour')
		`, teamID, artifactA, attemptA, profileA, contentHash); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_assessments (
				team_id, semantic_assessment_id, attempt_id, owner_profile_id,
				response_history, provider_turns, model
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, '[]'::jsonb, 1, 'rls-test')
		`, teamID, assessmentA, attemptA, profileA)
		return err
	})

	withProfile(teamID, profileA, func(tx *sql.Tx) error {
		for _, query := range []struct {
			name string
			sql  string
			id   string
		}{
			{"artifact", `SELECT count(*) FROM remember_failure_artifacts WHERE team_id = $1::uuid AND artifact_id = $2::uuid`, artifactA},
			{"assessment", `SELECT count(*) FROM semantic_assessments WHERE team_id = $1::uuid AND semantic_assessment_id = $2::uuid`, assessmentA},
		} {
			var count int
			if err := tx.QueryRowContext(ctx, query.sql, teamID, query.id).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("profile A can read control-only %s bytes", query.name)
			}
		}
		return nil
	})
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, query := range []struct {
			name string
			sql  string
			id   string
		}{
			{"artifact", `SELECT count(*) FROM remember_failure_artifacts WHERE team_id = $1::uuid AND artifact_id = $2::uuid`, artifactA},
			{"assessment", `SELECT count(*) FROM semantic_assessments WHERE team_id = $1::uuid AND semantic_assessment_id = $2::uuid`, assessmentA},
		} {
			var count int
			if err := tx.QueryRowContext(ctx, query.sql, teamID, query.id).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("system context cannot read %s row", query.name)
			}
		}
		return nil
	}))

	withProfile(teamID, profileB, func(tx *sql.Tx) error {
		for _, query := range []struct {
			name    string
			sql     string
			visible int
		}{
			{"attempt", `SELECT count(*) FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`, 1},
			{"event", `SELECT count(*) FROM remember_attempt_events WHERE team_id = $1::uuid AND event_id = $2::uuid`, 1},
			{"artifact", `SELECT count(*) FROM remember_failure_artifacts WHERE team_id = $1::uuid AND artifact_id = $2::uuid`, 0},
			{"assessment", `SELECT count(*) FROM semantic_assessments WHERE team_id = $1::uuid AND semantic_assessment_id = $2::uuid`, 0},
		} {
			var count int
			var id string
			switch query.name {
			case "attempt":
				id = attemptA
			case "event":
				id = eventA
			case "artifact":
				id = artifactA
			default:
				id = assessmentA
			}
			if err := tx.QueryRowContext(ctx, query.sql, teamID, id).Scan(&count); err != nil {
				return err
			}
			if count != query.visible {
				return fmt.Errorf("%s row visibility for same-team profile B = %d, want %d", query.name, count, query.visible)
			}
		}
		update, err := tx.ExecContext(ctx, `
			UPDATE remember_attempts SET outcome = 'completed'
			WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		`, teamID, attemptA)
		if err != nil {
			return err
		}
		rows, err := update.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 0 {
			return fmt.Errorf("profile B updated profile A attempt")
		}
		return nil
	})

	withProfile(teamC, profileC, func(tx *sql.Tx) error {
		for _, query := range []struct {
			name string
			sql  string
			id   string
		}{
			{"attempt", `SELECT count(*) FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`, attemptA},
			{"event", `SELECT count(*) FROM remember_attempt_events WHERE team_id = $1::uuid AND event_id = $2::uuid`, eventA},
			{"artifact", `SELECT count(*) FROM remember_failure_artifacts WHERE team_id = $1::uuid AND artifact_id = $2::uuid`, artifactA},
			{"assessment", `SELECT count(*) FROM semantic_assessments WHERE team_id = $1::uuid AND semantic_assessment_id = $2::uuid`, assessmentA},
		} {
			var count int
			if err := tx.QueryRowContext(ctx, query.sql, teamID, query.id).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("team C can see team A %s row", query.name)
			}
		}
		return nil
	})

	expectProfileInsertDeniedFor := func(currentTeamID, currentProfileID, allowedSpaceIDs, statement string, args ...any) {
		t.Helper()
		tx, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		defer tx.Rollback()
		require.NoError(t, func() error {
			if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole); err != nil {
				return err
			}
			for key, value := range map[string]string{
				"app.tx_mode": "profile", "app.current_team_id": currentTeamID, "app.current_profile_id": currentProfileID,
			} {
				if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, key, value); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `SELECT set_config('app.allowed_space_ids', $1, true)`, allowedSpaceIDs); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, statement, args...)
			if err == nil {
				return fmt.Errorf("profile %s inserted a row outside its owner scope", currentProfileID)
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				return fmt.Errorf("expected row-level-security denial for team %s/profile %s, got %w", currentTeamID, currentProfileID, err)
			}
			return nil
		}())
	}
	expectProfileInsertDenied := func(currentTeamID, currentProfileID, statement string, args ...any) {
		expectProfileInsertDeniedFor(currentTeamID, currentProfileID, "", statement, args...)
	}

	spaceAttemptID, spaceEventID := uuid.NewString(), uuid.NewString()
	withProfile(teamID, profileA, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.allowed_space_ids', $1, true)`, spaceA); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, contract_version, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1,
			          'rls-space-allowed', 'rls-space-allowed-request', 'dense-mem.v2.6', 'failed')
		`, teamID, spaceAttemptID, profileA, spaceA); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempt_events (
				team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 'commit', 'private-space')
		`, teamID, spaceEventID, spaceAttemptID, profileA)
		return err
	})
	withProfile(teamID, profileA, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.allowed_space_ids', $1, true)`, spaceA); err != nil {
			return err
		}
		for _, query := range []struct {
			name string
			sql  string
			id   string
		}{
			{"private attempt", `SELECT count(*) FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`, spaceAttemptID},
			{"private event", `SELECT count(*) FROM remember_attempt_events WHERE team_id = $1::uuid AND event_id = $2::uuid`, spaceEventID},
		} {
			var count int
			if err := tx.QueryRowContext(ctx, query.sql, teamID, query.id).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("profile A cannot read its allowed %s", query.name)
			}
		}
		return nil
	})
	withProfile(teamID, profileB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.allowed_space_ids', $1, true)`, spaceB); err != nil {
			return err
		}
		for _, query := range []struct {
			name string
			sql  string
			id   string
		}{
			{"private attempt", `SELECT count(*) FROM remember_attempts WHERE team_id = $1::uuid AND attempt_id = $2::uuid`, spaceAttemptID},
			{"private event", `SELECT count(*) FROM remember_attempt_events WHERE team_id = $1::uuid AND event_id = $2::uuid`, spaceEventID},
		} {
			var count int
			if err := tx.QueryRowContext(ctx, query.sql, teamID, query.id).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("profile B can read %s from a disallowed space", query.name)
			}
		}
		return nil
	})
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE memory_spaces SET generation = generation + 1
			WHERE id = $1::uuid
		`, spaceA)
		return err
	}))
	staleGenerationErr := execPostgresTxModeRollback(ctx, db, "profile", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_team_id', $1, true)`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_profile_id', $1, true)`, profileA); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.allowed_space_ids', $1, true)`, spaceA); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, contract_version, outcome
			) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 1,
			          'rls-space-stale-generation', 'rls-space-stale-generation-request', 'dense-mem.v2.6', 'failed')
		`, teamID, profileA, spaceA)
		return err
	})
	require.ErrorContains(t, staleGenerationErr, "memory space generation is stale")
	expectProfileInsertDeniedFor(teamID, profileA, spaceA, `
		INSERT INTO remember_attempts (
			team_id, attempt_id, owner_profile_id, space_id, space_generation,
			idempotency_key, request_hash, contract_version, outcome
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 1,
		          'rls-space-forbidden', 'rls-space-forbidden-request', 'dense-mem.v2.6', 'failed')
	`, teamID, profileA, spaceB)
	expectProfileInsertDeniedFor(teamID, profileA, spaceB, `
		INSERT INTO remember_attempt_events (
			team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 2, 'commit', 'wrong-space')
	`, teamID, spaceAttemptID, profileA)
	expectProfileInsertDeniedFor(teamID, profileA, spaceB, `
		INSERT INTO remember_failure_artifacts (
			team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
			content_type, content_bytes, byte_count, content_sha256, expires_at
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 'wrong-space',
		          'text/plain', decode('63', 'hex'), 1, $4,
		          now() + interval '1 hour')
	`, teamID, spaceAttemptID, profileA, contentHash)
	expectProfileInsertDeniedFor(teamID, profileA, spaceB, `
		INSERT INTO semantic_assessments (
			team_id, semantic_assessment_id, attempt_id, owner_profile_id,
			response_history, provider_turns, model
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, '[]'::jsonb, 1, 'wrong-space')
	`, teamID, spaceAttemptID, profileA)

	expectProfileInsertDenied(teamID, profileB, `
		INSERT INTO remember_attempts (
			team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
			contract_version, outcome
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, 'rls-b-forbidden-owner',
		          'rls-b-forbidden-owner-request', 'dense-mem.v2.6', 'failed')
	`, teamID, profileA)
	expectProfileInsertDenied(teamID, profileB, `
		INSERT INTO remember_attempt_events (
			team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 2, 'commit', 'forbidden')
	`, teamID, attemptA, profileA)
	expectProfileInsertDenied(teamID, profileB, `
		INSERT INTO remember_failure_artifacts (
			team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
			content_type, content_bytes, byte_count, content_sha256, expires_at
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 'forbidden',
		          'text/plain', decode('62', 'hex'), 1, $4,
		          now() + interval '1 hour')
	`, teamID, attemptA, profileA, contentHash)
	expectProfileInsertDenied(teamID, profileB, `
		INSERT INTO semantic_assessments (
			team_id, semantic_assessment_id, attempt_id, owner_profile_id,
			response_history, provider_turns, model
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, '[]'::jsonb, 1, 'forbidden')
	`, teamID, attemptA2, profileA)
	expectProfileInsertDenied(teamC, profileC, `
		INSERT INTO remember_attempts (
			team_id, attempt_id, owner_profile_id, idempotency_key, request_hash,
			contract_version, outcome
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, 'rls-c-forbidden-owner',
		          'rls-c-forbidden-owner-request', 'dense-mem.v2.6', 'failed')
	`, teamID, profileA)
	expectProfileInsertDenied(teamC, profileC, `
		INSERT INTO remember_attempt_events (
			team_id, event_id, attempt_id, owner_profile_id, sequence_no, phase, event_kind
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 2, 'commit', 'cross-team-forbidden')
	`, teamID, attemptA, profileA)
	expectProfileInsertDenied(teamC, profileC, `
		INSERT INTO remember_failure_artifacts (
			team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
			content_type, content_bytes, byte_count, content_sha256, expires_at
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 'cross-team-forbidden',
		          'text/plain', decode('62', 'hex'), 1, $4,
		          now() + interval '1 hour')
	`, teamID, attemptA, profileA, contentHash)
	expectProfileInsertDenied(teamC, profileC, `
		INSERT INTO semantic_assessments (
			team_id, semantic_assessment_id, attempt_id, owner_profile_id,
			response_history, provider_turns, model
		) VALUES ($1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, '[]'::jsonb, 1, 'cross-team-forbidden')
	`, teamID, attemptA2, profileA)

	var outcome string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT outcome FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, attemptA).Scan(&outcome))
	require.Equal(t, "failed", outcome)
}

func TestSynchronousWriteFoundationAppendOnlyRowsAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, synchronousWriteFoundationMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	attemptID := uuid.New()
	activeArtifactID, expiredArtifactID := uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, idempotency_key,
				request_hash, contract_version, outcome
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'foundation-key',
			          'foundation-request', 'dense-mem.v2.6', 'failed')
		`, teamID, attemptID, profileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempt_events (
				team_id, attempt_id, owner_profile_id, sequence_no,
				phase, event_kind, metadata
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 1,
			          'commit', 'failed', '{}'::jsonb)
		`, teamID, attemptID, profileID)
		if err != nil {
			return err
		}
		contentHash := "sha256:" + strings.Repeat("0", 64)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remember_failure_artifacts (
				team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
				content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'active',
			          'text/plain', decode('61', 'hex'), 1, $5,
			          CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + interval '1 hour'),
			       ($1::uuid, $6::uuid, $3::uuid, $4::uuid, 'expired',
			          'text/plain', decode('62', 'hex'), 1, $5,
			          CURRENT_TIMESTAMP - interval '8 days', CURRENT_TIMESTAMP - interval '1 hour')
		`, teamID, activeArtifactID, attemptID, profileID, contentHash, expiredArtifactID); err != nil {
			return err
		}
		return nil
	}))

	require.Error(t, execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM remember_failure_artifacts
			WHERE team_id = $1::uuid AND artifact_id = $2::uuid
		`, teamID, activeArtifactID)
		return err
	}))
	eventDeleteErr := execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM remember_attempt_events
			WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		`, teamID, attemptID)
		return err
	})
	require.Error(t, eventDeleteErr)
	require.Contains(t, eventDeleteErr.Error(), "remember_attempt_events is append-only")
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			DELETE FROM remember_failure_artifacts
			WHERE team_id = $1::uuid AND artifact_id = $2::uuid
		`, teamID, expiredArtifactID)
		return err
	}))
	systemUpdateErr := execPostgresTxModeRollback(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE remember_attempts SET error_code = 'migration_repair'
			WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		`, teamID, attemptID)
		return err
	})
	require.Error(t, systemUpdateErr)
	require.Contains(t, systemUpdateErr.Error(), "remember_attempts is append-only")
	require.NoError(t, execPostgresTxMode(ctx, db, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE remember_attempts SET error_code = 'migration_repair'
			WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		`, teamID, attemptID)
		return err
	}))
	var errorCode string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT error_code FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, attemptID).Scan(&errorCode))
	require.Equal(t, "migration_repair", errorCode)
	require.Error(t, execPostgresTxModeRollback(ctx, db, "profile", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE remember_attempts SET error_code = 'profile_tamper'
			WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		`, teamID, attemptID)
		return err
	}))
	require.Error(t, migrationDownTo(ctx, db, 20260823010001))
	require.True(t, tableExists(t, ctx, db, "remember_attempts"))
}
