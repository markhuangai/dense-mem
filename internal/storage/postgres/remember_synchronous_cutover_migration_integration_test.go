//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRememberSynchronousCutoverNormalizesLegacyReconciliationStatuses(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	contractID := uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model,
				dimensions, distance_metric, vector_normalization,
				document_format_version, query_format_version, lifecycle_state
			) VALUES ($1::uuid, $2, 1, 'test', 'legacy-reconciliation', 3,
			          'cosine', 'provider', 1, 1, 'active')
		`, contractID, "legacy-reconciliation-"+contractID.String()); err != nil {
			return err
		}
		for index, status := range []string{"reserved", "deferred", "ambiguous"} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO embedding_reconciliation_runs (
					embedding_contract_id, embedding_dimensions, local_run_date, status
				) VALUES ($1::uuid, 3, CURRENT_DATE - ($2::int * INTERVAL '1 day'), $3)
			`, contractID, index, status); err != nil {
				return err
			}
		}
		return nil
	}))

	runGooseUpTo(t, ctx, db, 20260825010001)
	rows, err := db.QueryContext(ctx, `
		SELECT status, last_error, completed_at IS NOT NULL
		FROM search_reconciliation_runs
		ORDER BY local_run_date DESC
	`)
	require.NoError(t, err)
	defer rows.Close()
	var count int
	for rows.Next() {
		var status, lastError string
		var completed bool
		require.NoError(t, rows.Scan(&status, &lastError, &completed))
		require.Equal(t, "failed", status)
		require.Contains(t, lastError, "legacy reconciliation status retired")
		require.True(t, completed)
		count++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 3, count)
}

func TestRememberSynchronousCutoverPreservesMigratedAttemptSpace(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	spaceID, ingestID := uuid.New(), uuid.New()
	const generation int64 = 7
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memory_spaces (id, team_id, kind, owner_profile_id, generation)
			VALUES ($1::uuid, $2::uuid, 'profile_private', $3::uuid, $4)
		`, spaceID, teamID, profileID, generation); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5,
			          'private-space-ingest', 'private-space-request', 'completed',
			          '{}'::jsonb, '{"_dense_mem_telemetry_origin":"remember"}'::jsonb, now())
		`, teamID, ingestID, profileID, spaceID, generation)
		return err
	}))

	runGooseUpTo(t, ctx, db, 20260825010001)
	var migratedSpaceID string
	var migratedGeneration int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT space_id::text, space_generation
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&migratedSpaceID, &migratedGeneration))
	require.Equal(t, spaceID.String(), migratedSpaceID)
	require.Equal(t, generation, migratedGeneration)
}

func TestRememberSynchronousCutoverPreservesTerminalHistoryBeforeRetirement(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID, runID, itemID, fragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	expiredIngestID, expiredRunID := uuid.New(), uuid.New()
	assessmentID, revisionID, outcomeID := uuid.New(), uuid.New(), uuid.New()
	claimKey := uuid.New()
	responseHash := "sha256:legacy-assessment"
	revisionHash := "sha256:legacy-repair"

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'cutover-history',
				        'cutover-history-request', 'completed',
				        '{"relationship_hints":[]}'::jsonb,
				        '{"_dense_mem_telemetry_origin":"remember","contract_version":"dense-mem.v2.5"}'::jsonb,
				        now())
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id,
				evidence_index, content, content_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          0, 'legacy cutover evidence', 'sha256:legacy-fragment')
		`, teamID, fragmentID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				status, attempts, max_attempts, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'completed', 1, 5, now())
		`, teamID, runID, ingestID, profileID); err != nil {
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
		`, teamID, itemID, runID, ingestID, profileID, fragmentID, claimKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_outcomes (
				team_id, outcome_id, placement_run_id, placement_item_id,
				owner_profile_id, outcome_kind, status, payload
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, 'relationship_result', 'completed', '{"stored":true}'::jsonb)
		`, teamID, outcomeID, runID, itemID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_assessments (
				team_id, assessment_id, placement_item_id, claim_key,
				owner_profile_id, request_id, assessor_contract_version,
				model, tokenizer, input_tokens, output_tokens,
				candidate_context_tokens, normalized_response, response_hash,
				validated_at, provider_turns
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, 'legacy-request', 'dense-mem.v2.5',
			          'legacy-model', 'legacy-tokenizer', 10, 5, 3,
			          '{"accepted":true}'::jsonb, $6, now(), 1)
		`, teamID, assessmentID, itemID, claimKey, profileID, responseHash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO submission_assessment_response_revisions (
				team_id, revision_id, assessment_id, ingest_id,
				placement_run_id, owner_profile_id, revision_number,
				provider_turns, input_tokens, output_tokens,
				candidate_context_tokens, normalized_response, response_hash,
				validated_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, $6::uuid, 1, 2, 12, 6, 4,
			          '{"repaired":true}'::jsonb, $7, now())
		`, teamID, revisionID, assessmentID, ingestID, runID, profileID, revisionHash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO predicate_registration_events (
				team_id, placement_run_id, assessment_id, owner_profile_id,
				relationship_ref, registration_action, predicate_key, predicate_version
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'legacy-ref', 'created', 'legacy_predicate', 1)
		`, teamID, runID, assessmentID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO submission_quarantine_payloads (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				proposal, evidence, assessor_response, expires_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          '{}'::jsonb, '[]'::jsonb, '{}'::jsonb, now() + interval '24 hours')
		`, teamID, runID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO submission_quarantine_tombstones (
				team_id, fragment_id, ingest_id, owner_profile_id, content_hash
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'sha256:legacy-fragment')
		`, teamID, fragmentID, ingestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'cutover-expired',
			          'cutover-expired-request', 'quarantined', '{}'::jsonb,
			          '{"_dense_mem_telemetry_origin":"remember"}'::jsonb,
			          now() - interval '8 days')
		`, teamID, expiredIngestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
				INSERT INTO placement_runs (
					team_id, placement_run_id, ingest_id, owner_profile_id,
					status, attempts, max_attempts, completed_at, quarantine_expires_at
				) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
				          'quarantined', 1, 5, now() - interval '8 days', now() - interval '7 days')
		`, teamID, expiredRunID, expiredIngestID, profileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO submission_quarantine_payloads (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				proposal, evidence, assessor_response, quarantined_at, expires_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          '{"old":true}'::jsonb, '[{"old":true}]'::jsonb, '{}'::jsonb,
			          now() - interval '8 days', now() - interval '7 days')
		`, teamID, expiredRunID, expiredIngestID, profileID)
		return err
	}))

	runGooseUpTo(t, ctx, db, 20260825010001)

	var outcome string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT outcome FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&outcome))
	require.Equal(t, "completed", outcome)

	var eventCount, itemEvents, outcomeEvents int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_attempt_events
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&eventCount))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE event_kind = 'legacy_item'),
		       count(*) FILTER (WHERE event_kind = 'legacy_outcome')
		FROM remember_attempt_events
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&itemEvents, &outcomeEvents))
	require.Equal(t, 5, eventCount)
	require.Equal(t, 1, itemEvents)
	require.Equal(t, 1, outcomeEvents)

	var quarantinePayloadEvents, quarantineTombstoneEvents int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE event_kind = 'legacy_quarantine_payload'),
		       count(*) FILTER (WHERE event_kind = 'legacy_quarantine_tombstone')
		FROM remember_attempt_events
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&quarantinePayloadEvents, &quarantineTombstoneEvents))
	require.Equal(t, 1, quarantinePayloadEvents)
	require.Equal(t, 1, quarantineTombstoneEvents)

	var artifactKind, artifactContentType, artifactPayloadHash string
	var artifactRetentionBounded bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT artifact_kind, content_type, convert_from(content_bytes, 'UTF8')::jsonb ->> 'payload_sha256',
		       expires_at = captured_at + interval '7 days'
		FROM remember_failure_artifacts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&artifactKind, &artifactContentType, &artifactPayloadHash, &artifactRetentionBounded))
	require.Equal(t, "legacy_submission_quarantine_payload", artifactKind)
	require.Equal(t, "application/json", artifactContentType)
	require.Empty(t, artifactPayloadHash)
	require.True(t, artifactRetentionBounded)

	var expiredArtifacts, expiredPayloadEvents int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_failure_artifacts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, expiredIngestID).Scan(&expiredArtifacts))
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_attempt_events
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
		  AND event_kind = 'legacy_quarantine_payload'
	`, teamID, expiredIngestID).Scan(&expiredPayloadEvents))
	require.Zero(t, expiredArtifacts)
	require.Equal(t, 1, expiredPayloadEvents)

	var historyCount, acceptedRevision int
	var semanticAssessmentID string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT semantic_assessment_id::text, jsonb_array_length(response_history), accepted_revision
		FROM semantic_assessments
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&semanticAssessmentID, &historyCount, &acceptedRevision))
	require.Equal(t, 2, historyCount)
	require.Equal(t, 1, acceptedRevision)
	require.NotEmpty(t, semanticAssessmentID)

	var mappedIngest, mappedAssessment string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT ingest_id::text, assessment_id::text
		FROM predicate_registration_events
		WHERE team_id = $1::uuid AND relationship_ref = 'legacy-ref'
	`, teamID).Scan(&mappedIngest, &mappedAssessment))
	require.Equal(t, ingestID.String(), mappedIngest)
	require.Equal(t, semanticAssessmentID, mappedAssessment)

	var markerVersion, markerStatus string
	var markerMetadata string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT version, status, metadata::text
		FROM v2_compatibility_markers
		WHERE marker_kind = 'v2_cutover'
		ORDER BY created_at DESC
		LIMIT 1
	`).Scan(&markerVersion, &markerStatus, &markerMetadata))
	require.Equal(t, "dense-mem.v2.6.1.cutover.v1", markerVersion)
	require.Equal(t, "compatible", markerStatus)
	require.Contains(t, markerMetadata, `"placement_item_count": 1`)
	require.Contains(t, markerMetadata, `"placement_outcome_count": 1`)
	require.Contains(t, markerMetadata, `"assessment_history_count": 2`)

	for _, retiredTable := range []string{
		"placement_runs", "placement_items", "placement_outcomes", "placement_assessments",
		"embedding_jobs", "remember_source_revision_intents", "remember_supersession_intents",
		"submission_assessment_response_revisions", "submission_quarantine_payloads",
		"submission_quarantine_tombstones",
	} {
		require.False(t, tableExists(t, ctx, db, retiredTable), "%s should be dropped", retiredTable)
	}
	var reconciliationColumns int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'search_reconciliation_runs'
		  AND column_name = ANY($1::text[])
	`, []string{"selected_count", "embedded_count", "updated_count", "drifted_count"}).Scan(&reconciliationColumns))
	require.Equal(t, 4, reconciliationColumns)
	for _, retiredColumn := range []string{
		"candidate_cutoff", "worker_id", "lease_token", "lease_until",
		"canary_job_id", "canary_attempted_at", "canary_outcome",
		"canary_failure_class", "canary_failure_code",
	} {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'search_reconciliation_runs' AND column_name = $1
			)
		`, retiredColumn).Scan(&exists))
		require.False(t, exists, "%s should be retired", retiredColumn)
	}

	var retiredForeignKeys int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS parent ON parent.oid = constraint_row.confrelid
		WHERE constraint_row.contype = 'f'
		  AND parent.relname = ANY($1::text[])
	`, []string{
		"placement_runs", "placement_items", "placement_outcomes", "placement_assessments",
		"embedding_jobs", "remember_source_revision_intents", "remember_supersession_intents",
		"submission_assessment_response_revisions", "submission_quarantine_payloads",
		"submission_quarantine_tombstones",
	}).Scan(&retiredForeignKeys))
	require.Zero(t, retiredForeignKeys)
}

func TestRememberSynchronousCutoverRejectsUnknownPlacementOrigin(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID, runID := uuid.New(), uuid.New()
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
		_, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				status, attempts, max_attempts, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'completed', 1, 5, now())
		`, teamID, runID, ingestID, profileID)
		return err
	}))

	err := migrationUpTo(ctx, db, 20260825010001)
	require.Error(t, err)
	require.ErrorContains(t, err, "placement runs have an unknown origin")
	require.True(t, tableExists(t, ctx, db, "placement_runs"))
	require.True(t, tableExists(t, ctx, db, "embedding_jobs"))
}

func TestRememberSynchronousCutoverTerminalizesPendingCorrections(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	submissionID, relationshipID := uuid.New(), uuid.New()
	subjectID, objectID := uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_records (team_id, entity_id, entity_kind)
			VALUES ($1::uuid, $2::uuid, 'project'), ($1::uuid, $3::uuid, 'product')
		`, teamID, subjectID, objectID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, relationship_kind, current_cardinality
			) VALUES ($1::uuid, 'works_on', 1, 'state', 'many')
		`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_records (
				team_id, relationship_id, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id,
				relationship_kind, current_cardinality, status, support_count
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'cutover-correction',
				$4::uuid, 'works_on', 1, $5::uuid,
				'state', 'many', 'active', 1
			)
		`, teamID, relationshipID, profileID, subjectID, objectID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO relationship_correction_submissions (
				team_id, submission_id, owner_profile_id, relationship_id,
				expected_version, request_hash, patch, supports, reason, idempotency_key,
				processing_state, confirmation_token, confirmation_expires_at, candidates
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				1, 'pending-correction-hash', '{}'::jsonb, '[]'::jsonb,
				'pending correction', 'pending-correction-key', 'awaiting_confirmation',
				'confirmation-token', now() + interval '1 hour', '[{"candidate":"a"},{"candidate":"b"}]'::jsonb
			)
		`, teamID, submissionID, profileID, relationshipID)
		return err
	}))

	runGooseUpTo(t, ctx, db, 20260825010001)
	var state, errorCode, errorMessage, token string
	var completedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT processing_state, error_code, error_message, confirmation_token, completed_at
		FROM relationship_correction_submissions
		WHERE team_id = $1::uuid AND submission_id = $2::uuid
	`, teamID, submissionID).Scan(&state, &errorCode, &errorMessage, &token, &completedAt))
	require.Equal(t, "failed", state)
	require.Equal(t, "contract_superseded", errorCode)
	require.Contains(t, errorMessage, "retry the correction")
	require.Empty(t, token)
	require.True(t, completedAt.Valid)
}

func TestRememberSynchronousCutoverRejectsActivePlacementRuns(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"queued", "guarded", "processing"} {
		t.Run(status, func(t *testing.T) {
			db, cleanup := openMigrationSQLDB(t, ctx)
			defer cleanup()
			runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
			teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
			ingestID, runID := uuid.New(), uuid.New()
			require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO knowledge_ingests (
						team_id, ingest_id, owner_profile_id, idempotency_key,
						request_hash, status, proposal, metadata
					) VALUES ($1::uuid, $2::uuid, $3::uuid, 'active-run-key',
						        'active-run-request', $4, '{}'::jsonb,
						        '{"_dense_mem_telemetry_origin":"remember","contract_version":"dense-mem.v2.6"}'::jsonb)
				`, teamID, ingestID, profileID, status); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `
					INSERT INTO placement_runs (
						team_id, placement_run_id, ingest_id, owner_profile_id,
						status, attempts, max_attempts
					) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, 0, 5)
				`, teamID, runID, ingestID, profileID, status)
				return err
			}))

			err := migrationUpTo(ctx, db, 20260825010001)
			require.Error(t, err)
			require.Contains(t, err.Error(), "placement runs are still active")
			require.True(t, tableExists(t, ctx, db, "placement_runs"))
		})
	}
}

func TestRememberSynchronousCutoverRejectsActiveEmbeddingJobs(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"queued", "processing"} {
		t.Run(status, func(t *testing.T) {
			db, cleanup := openMigrationSQLDB(t, ctx)
			defer cleanup()
			runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
			teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
			contractID, generationID, documentID, sourceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			jobID := uuid.New()
			require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO embedding_contracts (
						embedding_contract_id, contract_key, version, provider, model,
						dimensions, distance_metric, vector_normalization,
						document_format_version, query_format_version, lifecycle_state
					) VALUES ($1::uuid, $2, 1, 'test', 'cutover-model', 3,
					          'cosine', 'provider', 1, 1, 'active')
				`, contractID, "cutover-job-"+contractID.String()); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO search_index_generations (
						search_index_generation_id, generation, embedding_contract_id,
						embedding_dimensions, ann_strategy, activation_state, activated_at
					) VALUES ($1::uuid, 1, $2::uuid, 3, 'exact', 'active', now())
				`, generationID, contractID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO search_documents (
						team_id, search_document_id, owner_profile_id, source_kind, source_id,
						source_version, document_version, embedding_contract_id,
						embedding_dimensions, search_state, document_text, document_hash
					) VALUES ($1::uuid, $2::uuid, $3::uuid, 'evidence', $4::uuid,
					          1, 1, $5::uuid, 3, 'pending', 'active job document', 'sha256:active-job')
				`, teamID, documentID, profileID, sourceID, contractID); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `
					INSERT INTO embedding_jobs (
						team_id, embedding_job_id, search_document_id, owner_profile_id,
						source_kind, source_id, source_version, document_version,
						embedding_contract_id, embedding_dimensions, status, attempts, max_attempts
					) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
					          'evidence', $5::uuid, 1, 1, $6::uuid, 3, $7, 0, 5)
				`, teamID, jobID, documentID, profileID, sourceID, contractID, status)
				return err
			}))

			err := migrationUpTo(ctx, db, 20260825010001)
			require.Error(t, err)
			require.Contains(t, err.Error(), "embedding jobs are still active")
			require.True(t, tableExists(t, ctx, db, "embedding_jobs"))
		})
	}
}

func TestRememberSynchronousCutoverRejectsInvalidActiveSearchDocuments(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		state      string
		embedding  string
		dropChecks bool
	}{
		{name: "missing-vector", state: "current"},
		{name: "non-current", state: "pending"},
		{name: "wrong-dimension", state: "current", embedding: "'[1,2]'::vector", dropChecks: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, cleanup := openMigrationSQLDB(t, ctx)
			defer cleanup()
			runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
			teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
			contractID, generationID, documentID, sourceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
				if test.dropChecks {
					if _, err := tx.ExecContext(ctx, `ALTER TABLE search_documents DROP CONSTRAINT IF EXISTS search_documents_embedding_dims_check`); err != nil {
						return err
					}
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO embedding_contracts (
						embedding_contract_id, contract_key, version, provider, model,
						dimensions, distance_metric, vector_normalization,
						document_format_version, query_format_version, lifecycle_state
					) VALUES ($1::uuid, $2, 1, 'test', 'cutover-model', 3,
					          'cosine', 'provider', 1, 1, 'active')
				`, contractID, "cutover-document-"+contractID.String()); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO search_index_generations (
						search_index_generation_id, generation, embedding_contract_id,
						embedding_dimensions, ann_strategy, activation_state, activated_at
					) VALUES ($1::uuid, 1, $2::uuid, 3, 'exact', 'active', now())
				`, generationID, contractID); err != nil {
					return err
				}
				embeddingSQL := "NULL"
				if test.embedding != "" {
					embeddingSQL = test.embedding
				}
				query := `
					INSERT INTO search_documents (
						team_id, search_document_id, owner_profile_id, source_kind, source_id,
						source_version, document_version, embedding_contract_id,
						embedding_dimensions, search_state, document_text, document_hash, embedding
					) VALUES ($1::uuid, $2::uuid, $3::uuid, 'evidence', $4::uuid,
					          1, 1, $5::uuid, 3, $6, 'invalid active document', 'sha256:invalid-active', %s)
				`
				_, err := tx.ExecContext(ctx, fmt.Sprintf(query, embeddingSQL), teamID, documentID, profileID, sourceID, contractID, test.state)
				return err
			}))

			err := migrationUpTo(ctx, db, 20260825010001)
			require.Error(t, err)
			require.Contains(t, err.Error(), "active-contract search documents are not current")
			require.True(t, tableExists(t, ctx, db, "search_documents"))
		})
	}
}

func TestRememberSynchronousAssessmentForeignKeyDefersUntilTerminalAttempt(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	runGooseUpTo(t, ctx, db, 20260825010001)
	ingestID, assessmentID := uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'deferred-fk-ingest',
				        'deferred-fk-request', 'completed', '{}'::jsonb,
				        '{"_dense_mem_telemetry_origin":"remember"}'::jsonb, now())
		`, teamID, ingestID, profileID); err != nil {
			return err
		}
		// The application commit writes assessment history before the terminal
		// attempt payload is complete; the FK must validate at transaction end.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO semantic_assessments (
				team_id, semantic_assessment_id, attempt_id, owner_profile_id,
				response_history, accepted_revision, provider_turns, model, tokenizer,
				input_tokens, output_tokens, candidate_context_tokens, response_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          '[{}]'::jsonb, 1, 1, 'model', 'tokenizer', 1, 1, 1, 'sha256:assessment')
		`, teamID, assessmentID, ingestID, profileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, idempotency_key,
				request_hash, contract_version, submission_kind, outcome,
				public_result, canonical_attempt_id, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'deferred-fk-attempt',
			          'deferred-fk-request', 'dense-mem.v2.6.1', 'remember', 'completed',
			          '{}'::jsonb, $2::uuid, now())
		`, teamID, ingestID, profileID)
		return err
	}))
}
