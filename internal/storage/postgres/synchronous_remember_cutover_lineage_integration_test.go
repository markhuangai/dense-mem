//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRememberSynchronousCutoverRejectsExistingMarkerConflict(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO v2_compatibility_markers (
				marker_kind, version, status, corpus_hash, gate_report_hash, metadata
			) VALUES (
				'v2_cutover', 'dense-mem.v2.6.1.cutover.v1', 'incompatible',
				'sha256:fixture', 'sha256:fixture', '{"fixture":"marker-conflict"}'::jsonb
			)
		`)
		return err
	}))

	err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
	require.ErrorContains(t, err, "v2.6.1 compatible marker already exists")
	require.True(t, tableExists(t, ctx, db, "placement_runs"))
	var markerStatus string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT status
		FROM v2_compatibility_markers
		WHERE marker_kind = 'v2_cutover'
		  AND version = 'dense-mem.v2.6.1.cutover.v1'
	`).Scan(&markerStatus))
	require.Equal(t, "incompatible", markerStatus)
	var cutoverApplied bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM goose_db_version
			WHERE version_id = $1 AND is_applied
		)
	`, synchronousRememberCutoverMigrationVersion).Scan(&cutoverApplied))
	require.False(t, cutoverApplied)
}

func TestRememberSynchronousCutoverRejectsConflictingAssessmentMapping(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, synchronousWriteFoundationMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	attemptA, attemptB := uuid.New(), uuid.New()
	semanticAssessmentA, semanticAssessmentB := uuid.New(), uuid.New()
	legacyAssessmentID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, attemptID := range []uuid.UUID{attemptA, attemptB} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO remember_attempts (
					team_id, attempt_id, owner_profile_id, idempotency_key,
					request_hash, contract_version, submission_kind, outcome
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4,
				          $5, 'dense-mem.v2.6', 'remember', 'completed')
			`, teamID, attemptID, profileID, "mapping-"+attemptID.String(), "mapping-request-"+attemptID.String()); err != nil {
				return err
			}
		}
		for _, assessmentID := range []uuid.UUID{semanticAssessmentA, semanticAssessmentB} {
			attemptID := attemptA
			if assessmentID == semanticAssessmentB {
				attemptID = attemptB
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO semantic_assessments (
					team_id, semantic_assessment_id, attempt_id, owner_profile_id,
					response_history, accepted_revision, provider_turns, model,
					response_hash, validated_at
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
				          jsonb_build_array(jsonb_build_object('assessment_id', $5::text, 'revision_number', 1)),
				          1, 1, 'mapping-conflict', $6, now())
			`, teamID, assessmentID, attemptID, profileID, legacyAssessmentID, "sha256:"+strings.Repeat("c", 64)); err != nil {
				return err
			}
		}
		return nil
	}))

	err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
	require.ErrorContains(t, err, "assessment IDs have conflicting semantic mappings")
	require.True(t, tableExists(t, ctx, db, "placement_runs"))
	var markerCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM v2_compatibility_markers
		WHERE marker_kind = 'v2_cutover'
		  AND version = 'dense-mem.v2.6.1.cutover.v1'
	`).Scan(&markerCount))
	require.Zero(t, markerCount)
	var assessmentCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM semantic_assessments
		WHERE team_id = $1::uuid
	`, teamID).Scan(&assessmentCount))
	require.Equal(t, 2, assessmentCount)
}

func TestRememberSynchronousCutoverMapsOrphanedPredicateRegistrationThroughAssessment(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID, staleRunID, assessmentID, itemID, fragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	claimKey := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'predicate-orphan',
			          'predicate-orphan-request', 'completed', '{}'::jsonb,
			          '{"_dense_mem_telemetry_origin":"remember"}'::jsonb, now())
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DO $drop_legacy_run_fks$
			DECLARE
				target_table REGCLASS;
				foreign_key RECORD;
			BEGIN
				FOREACH target_table IN ARRAY ARRAY[
					'placement_assessments'::regclass,
					'placement_items'::regclass,
					'predicate_registration_events'::regclass
				] LOOP
					FOR foreign_key IN
						SELECT constraint_row.conname
						FROM pg_constraint AS constraint_row
						WHERE constraint_row.conrelid = target_table
						  AND constraint_row.confrelid = 'placement_runs'::regclass
						  AND constraint_row.contype = 'f'
					LOOP
						EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', target_table, foreign_key.conname);
					END LOOP;
				END LOOP;
			END
			$drop_legacy_run_fks$;
		`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id,
				evidence_index, content, content_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          0, 'orphan predicate evidence', 'sha256:orphan-predicate-evidence')
		`, teamID, fragmentID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_items (
				team_id, placement_item_id, placement_run_id, ingest_id,
				owner_profile_id, fragment_id, evidence_index, claim_key,
				status, category, result
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, $6::uuid, 0, $7::uuid,
			          'completed', 'validated_claim', '{"accepted":true}'::jsonb)
		`, teamID, itemID, staleRunID, ingestID, profileID, fragmentID, claimKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_assessments (
				team_id, assessment_id, owner_profile_id, request_id,
				assessor_contract_version, model, tokenizer,
				input_tokens, output_tokens, candidate_context_tokens,
				normalized_response, response_hash, validated_at, provider_turns,
				assessment_scope, placement_run_id, ingest_id
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'predicate-orphan-request',
			          'dense-mem.v2.6', 'legacy-model', 'legacy-tokenizer',
			          10, 5, 3, '{"accepted":true}'::jsonb,
			          'sha256:' || repeat('a', 64), now(), 1,
			          'submission', $4::uuid, $5::uuid)
		`, teamID, assessmentID, profileID, staleRunID, ingestID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO predicate_registration_events (
				team_id, placement_run_id, assessment_id, owner_profile_id,
				relationship_ref, registration_action, predicate_key,
				predicate_version, metadata
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'predicate-orphan-ref', 'created', 'legacy_orphan_predicate',
			          1, '{"legacy_note":"preserve"}'::jsonb)
		`, teamID, staleRunID, assessmentID, profileID)
		return err
	}))

	runGooseUpTo(t, ctx, db, synchronousRememberCutoverMigrationVersion)

	var mappedIngest, mappedAssessment, legacyRunID, legacyAssessmentID, legacyNote string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT ingest_id::text, assessment_id::text,
		       metadata ->> 'legacy_placement_run_id',
		       metadata ->> 'legacy_assessment_id',
		       metadata ->> 'legacy_note'
		FROM predicate_registration_events
		WHERE team_id = $1::uuid AND relationship_ref = 'predicate-orphan-ref'
	`, teamID).Scan(&mappedIngest, &mappedAssessment, &legacyRunID, &legacyAssessmentID, &legacyNote))
	require.Equal(t, ingestID.String(), mappedIngest)
	require.Equal(t, staleRunID.String(), legacyRunID)
	require.Equal(t, assessmentID.String(), legacyAssessmentID)
	require.Equal(t, "preserve", legacyNote)

	var semanticAttempt, semanticOwner string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT attempt_id::text, owner_profile_id::text
		FROM semantic_assessments
		WHERE team_id = $1::uuid AND semantic_assessment_id = $2::uuid
	`, teamID, mappedAssessment).Scan(&semanticAttempt, &semanticOwner))
	require.Equal(t, ingestID.String(), semanticAttempt)
	require.Equal(t, profileID, semanticOwner)
}

func TestRememberSynchronousCutoverRejectsConflictingPredicateRegistrationMapping(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	firstIngestID, secondIngestID := uuid.New(), uuid.New()
	firstRunID, secondRunID, assessmentID := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES
				($1::uuid, $2::uuid, $3::uuid, 'predicate-conflict-a',
				 'predicate-conflict-request-a', 'failed', '{}'::jsonb,
				 '{"_dense_mem_telemetry_origin":"remember"}'::jsonb, now()),
				($1::uuid, $4::uuid, $3::uuid, 'predicate-conflict-b',
				 'predicate-conflict-request-b', 'failed', '{}'::jsonb,
				 '{"_dense_mem_telemetry_origin":"remember"}'::jsonb, now())
		`, teamID, firstIngestID, profileID, secondIngestID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				status, attempts, max_attempts, completed_at
			) VALUES
				($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'failed', 1, 5, now()),
				($1::uuid, $5::uuid, $6::uuid, $4::uuid, 'failed', 1, 5, now())
		`, teamID, firstRunID, firstIngestID, profileID, secondRunID, secondIngestID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_assessments (
				team_id, assessment_id, owner_profile_id, request_id,
				assessor_contract_version, model, tokenizer,
				input_tokens, output_tokens, candidate_context_tokens,
				normalized_response, response_hash, validated_at, provider_turns,
				assessment_scope, placement_run_id, ingest_id
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'predicate-conflict-assessment',
			          'dense-mem.v2.6', 'legacy-model', 'legacy-tokenizer',
			          10, 5, 3, '{"accepted":true}'::jsonb,
			          'sha256:' || repeat('b', 64), now(), 1,
			          'submission', $4::uuid, $5::uuid)
		`, teamID, assessmentID, profileID, secondRunID, secondIngestID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO predicate_registration_events (
				team_id, placement_run_id, assessment_id, owner_profile_id,
				relationship_ref, registration_action, predicate_key, predicate_version
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'predicate-conflicting-ref', 'created', 'legacy_conflicting_predicate', 1)
		`, teamID, firstRunID, assessmentID, profileID)
		return err
	}))

	err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
	require.ErrorContains(t, err, "predicate registration event mappings disagree with assessment history")
	require.True(t, tableExists(t, ctx, db, "placement_runs"))
	require.True(t, tableExists(t, ctx, db, "predicate_registration_events"))
}

func TestRememberSynchronousCutoverRejectsBareUnknownOrigin(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'unknown-origin',
				        'unknown-origin-request', 'completed', '{}'::jsonb, now())
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		return nil
	}))

	err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
	require.ErrorContains(t, err, "ingests have an unknown origin")
	var ingestCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM knowledge_ingests
		WHERE team_id = $1::uuid AND ingest_id = $2::uuid
	`, teamID, ingestID).Scan(&ingestCount))
	require.Equal(t, 1, ingestCount)
	var markerCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM v2_compatibility_markers
		WHERE marker_kind = 'v2_cutover'
		  AND version = 'dense-mem.v2.6.1.cutover.v1'
	`).Scan(&markerCount))
	require.Zero(t, markerCount)
	require.True(t, tableExists(t, ctx, db, "placement_runs"))
	require.True(t, tableExists(t, ctx, db, "embedding_jobs"))
}
