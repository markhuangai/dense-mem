//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/stretchr/testify/require"
)

const synchronousRememberCutoverMigrationVersion int64 = 20260831010001

func TestRememberSynchronousCutoverPreservesTerminalHistoryBeforeRetirement(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID, runID, itemID, fragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	expiredIngestID, expiredRunID := uuid.New(), uuid.New()
	rejectedIngestID, rejectedRunID, rejectedItemID, rejectedFragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	assessmentID, revisionID, outcomeID, relationshipID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	claimKey := uuid.New()
	responseHash := "sha256:" + strings.Repeat("a", 64)
	revisionHash := "sha256:" + strings.Repeat("b", 64)

	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'cutover-history',
			          'cutover-history-request', 'completed',
			          '{"relationship_hints":[{"ref":"legacy-ref"}]}'::jsonb,
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
			INSERT INTO submission_relationship_results (
				team_id, ingest_id, placement_run_id, owner_profile_id,
				relationship_ref, disposition, reason, splits
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'legacy-ref', 'stored', '',
			          jsonb_build_array(jsonb_build_object(
			              'split_index', 0,
			              'relationship_id', $5::text,
			              'relationship_version', 1,
			              'status', 'active')))
		`, teamID, ingestID, runID, profileID, relationshipID); err != nil {
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
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'cutover-rejected',
			          'cutover-rejected-request', 'rejected',
			          '{"relationship_hints":[]}'::jsonb,
			          '{"_dense_mem_telemetry_origin":"remember"}'::jsonb,
			          now())
		`, teamID, rejectedIngestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id,
				evidence_index, content, content_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          0, 'legacy rejected evidence', 'sha256:legacy-rejected')
		`, teamID, rejectedFragmentID, rejectedIngestID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO placement_runs (
				team_id, placement_run_id, ingest_id, owner_profile_id,
				status, attempts, max_attempts, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          'rejected', 1, 5, now())
		`, teamID, rejectedRunID, rejectedIngestID, profileID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO placement_items (
				team_id, placement_item_id, placement_run_id, ingest_id,
				owner_profile_id, fragment_id, evidence_index, claim_key,
				status, category, result
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, $6::uuid, 0, $7::uuid,
			          'rejected', 'rejected', '{}'::jsonb)
		`, teamID, rejectedItemID, rejectedRunID, rejectedIngestID, profileID, rejectedFragmentID, rejectedItemID)
		return err
	}))

	runGooseUpTo(t, ctx, db, synchronousRememberCutoverMigrationVersion)

	var outcome, attemptContractVersion, publicContractVersion string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT outcome, contract_version, public_result ->> 'contract_version' FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&outcome, &attemptContractVersion, &publicContractVersion))
	require.Equal(t, "completed", outcome)
	require.Equal(t, "remember_request_hash_v1", attemptContractVersion)
	require.Equal(t, "dense-mem.v2.6.1", publicContractVersion)
	var evidenceDisposition, evidenceID, correlationID string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT public_result -> 'evidence' -> 0 ->> 'disposition',
		       public_result -> 'evidence' -> 0 ->> 'evidence_id',
		       public_result ->> 'correlation_id'
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&evidenceDisposition, &evidenceID, &correlationID))
	require.Equal(t, "stored", evidenceDisposition)
	require.Equal(t, fragmentID.String(), evidenceID)
	require.Equal(t, ingestID.String(), correlationID)
	var migratedCompletedJSON []byte
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT public_result
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&migratedCompletedJSON))
	var migratedCompleted remember.TerminalRememberResult
	require.NoError(t, json.Unmarshal(migratedCompletedJSON, &migratedCompleted))
	migratedCompleted.Kind = remember.ResultKindTerminal
	require.NoError(t, remember.ValidateTerminalRememberResult(&migratedCompleted, 1, []string{"legacy-ref"}))

	var rejectedDisposition, rejectedReason, rejectedErrorCode string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT public_result -> 'evidence' -> 0 ->> 'disposition',
		       public_result -> 'evidence' -> 0 ->> 'reason',
		       public_result -> 'errors' -> 0 ->> 'code'
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, rejectedIngestID).Scan(&rejectedDisposition, &rejectedReason, &rejectedErrorCode))
	require.Equal(t, "not_stored", rejectedDisposition)
	require.Equal(t, "not_supported_by_evidence", rejectedReason)
	require.Equal(t, "no_supported_memory", rejectedErrorCode)
	var migratedRejectedJSON []byte
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT public_result
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, rejectedIngestID).Scan(&migratedRejectedJSON))
	var migratedRejected remember.TerminalRememberResult
	require.NoError(t, json.Unmarshal(migratedRejectedJSON, &migratedRejected))
	migratedRejected.Kind = remember.ResultKindTerminal
	require.NoError(t, remember.ValidateTerminalRememberResult(&migratedRejected, 1, nil))

	var expiredErrorCode string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT public_result -> 'errors' -> 0 ->> 'code'
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, expiredIngestID).Scan(&expiredErrorCode))
	require.Equal(t, "submission_quarantined", expiredErrorCode)
	var migratedQuarantinedJSON []byte
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT public_result
		FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, expiredIngestID).Scan(&migratedQuarantinedJSON))
	var migratedQuarantined remember.TerminalRememberResult
	require.NoError(t, json.Unmarshal(migratedQuarantinedJSON, &migratedQuarantined))
	migratedQuarantined.Kind = remember.ResultKindTerminal
	require.NoError(t, remember.ValidateTerminalRememberResult(&migratedQuarantined, 0, nil))

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
	var assessmentFKDeferrable, assessmentFKDeferred bool
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT condeferrable, condeferred
		FROM pg_constraint
		WHERE conrelid = 'semantic_assessments'::regclass
		  AND confrelid = 'remember_attempts'::regclass
		  AND conname = 'semantic_assessments_team_id_attempt_id_owner_profile_id_fkey'
	`).Scan(&assessmentFKDeferrable, &assessmentFKDeferred))
	require.True(t, assessmentFKDeferrable)
	require.True(t, assessmentFKDeferred)

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
	require.Contains(t, markerMetadata, `"placement_item_count": 2`)
	require.Contains(t, markerMetadata, `"placement_outcome_count": 1`)
	require.Contains(t, markerMetadata, `"assessment_history_count": 2`)

	for _, retiredTable := range []string{
		"placement_runs", "placement_items", "placement_outcomes", "placement_assessments",
		"embedding_jobs", "remember_source_revision_intents", "remember_supersession_intents",
		"submission_assessment_response_revisions", "submission_quarantine_payloads",
		"submission_quarantine_tombstones",
		"telemetry_first_disposition_backfill_state",
	} {
		require.False(t, tableExists(t, ctx, db, retiredTable), "%s should be dropped", retiredTable)
	}
	for _, retainedTable := range []string{"entity_correction_plans", "evidence_quarantines"} {
		require.True(t, tableExists(t, ctx, db, retainedTable), "%s must remain durable", retainedTable)
	}
	for _, tableName := range []string{
		"remember_attempts", "remember_attempt_events", "remember_failure_artifacts", "semantic_assessments",
	} {
		var forced bool
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT relforcerowsecurity
			FROM pg_class
			WHERE oid = $1::regclass
		`, tableName).Scan(&forced))
		require.True(t, forced, "%s must force row-level security", tableName)
	}
	for _, retiredColumn := range []struct {
		table  string
		column string
	}{
		{table: "relationship_observations", column: "placement_item_id"},
		{table: "entity_resolution_events", column: "placement_item_id"},
		{table: "review_tasks", column: "placement_item_id"},
		{table: "submission_relationship_results", column: "placement_run_id"},
		{table: "predicate_registration_events", column: "placement_run_id"},
	} {
		require.False(t, columnExists(t, ctx, db, retiredColumn.table, retiredColumn.column),
			"%s.%s should be retired", retiredColumn.table, retiredColumn.column)
	}
	for _, retiredIndex := range []string{
		"submission_relationship_results_submission_idx",
		"embedding_jobs_ready_idx", "embedding_jobs_lease_idx", "embedding_jobs_contract_status_idx",
		"embedding_jobs_reconciliation_failed_idx", "embedding_jobs_failure_groups_idx",
		"embedding_jobs_projection_generation_idx", "embedding_jobs_terminal_retention_idx",
	} {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT to_regclass('public.' || $1) IS NOT NULL
		`, retiredIndex).Scan(&exists))
		require.False(t, exists, "%s should be retired", retiredIndex)
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
		"telemetry_first_disposition_backfill_state",
	}).Scan(&retiredForeignKeys))
	require.Zero(t, retiredForeignKeys)
	var semanticAssessmentForeignKeys int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS child ON child.oid = constraint_row.conrelid
		JOIN pg_class AS parent ON parent.oid = constraint_row.confrelid
		WHERE constraint_row.contype = 'f'
		  AND parent.relname = 'semantic_assessments'
		  AND child.relname = ANY($1::text[])
	`, []string{"entity_resolution_events", "verification_events", "review_tasks", "predicate_registration_events"}).Scan(&semanticAssessmentForeignKeys))
	require.Equal(t, 4, semanticAssessmentForeignKeys)
}

func TestRememberSynchronousCutoverNormalizesLegacyReconciliationStatuses(t *testing.T) {
	ctx := context.Background()
	for _, legacyStatus := range []string{"reserved", "deferred", "ambiguous"} {
		t.Run(legacyStatus, func(t *testing.T) {
			db, cleanup := openMigrationSQLDB(t, ctx)
			defer cleanup()

			runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
			contractID, runID := uuid.New(), uuid.New()
			require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO embedding_contracts (
						embedding_contract_id, contract_key, version, provider, model,
						dimensions, distance_metric, vector_normalization,
						document_format_version, query_format_version, lifecycle_state
					) VALUES ($1::uuid, $2, 1, 'test', 'cutover-reconciliation-model', 3,
					          'cosine', 'provider', 1, 1, 'active')
				`, contractID, "cutover-reconciliation-"+contractID.String()); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `
					INSERT INTO embedding_reconciliation_runs (
						reconciliation_run_id, embedding_contract_id, embedding_dimensions,
						local_run_date, status, last_error
					) VALUES ($1::uuid, $2::uuid, 3, CURRENT_DATE, $3, '')
				`, runID, contractID, legacyStatus)
				return err
			}))

			runGooseUpTo(t, ctx, db, synchronousRememberCutoverMigrationVersion)

			var status, lastError string
			var completedAt sql.NullTime
			require.NoError(t, db.QueryRowContext(ctx, `
				SELECT status, last_error, completed_at
				FROM search_reconciliation_runs
				WHERE reconciliation_run_id = $1::uuid
			`, runID).Scan(&status, &lastError, &completedAt))
			require.Equal(t, "failed", status)
			require.Contains(t, lastError, "legacy status "+legacyStatus)
			require.True(t, completedAt.Valid)
			var markerCount int
			require.NoError(t, db.QueryRowContext(ctx, `
				SELECT count(*)
				FROM v2_compatibility_markers
				WHERE marker_kind = 'v2_cutover'
				  AND version = 'dense-mem.v2.6.1.cutover.v1'
			`).Scan(&markerCount))
			require.Equal(t, 1, markerCount)
		})
	}
}

func TestRememberSynchronousCutoverRollsBackCopiedHistoryOnGateFailure(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID, runID, itemID, fragmentID, claimKey := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	contractID, generationID, documentID, sourceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'cutover-rollback',
				        'cutover-rollback-request', 'completed', '{}'::jsonb,
				        '{"_dense_mem_telemetry_origin":"remember"}'::jsonb, now())
		`, teamID, ingestID, profileID); err != nil {
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
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id,
				evidence_index, content, content_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          0, 'rollback evidence', 'sha256:rollback-evidence')
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
		`, teamID, itemID, runID, ingestID, profileID, fragmentID, claimKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model,
				dimensions, distance_metric, vector_normalization,
				document_format_version, query_format_version, lifecycle_state
			) VALUES ($1::uuid, $2, 1, 'test', 'cutover-rollback-model', 3,
			          'cosine', 'provider', 1, 1, 'active')
		`, contractID, "cutover-rollback-"+contractID.String()); err != nil {
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
		_, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'evidence', $4::uuid,
			          1, 1, $5::uuid, 3, 'current', 'rollback document', 'sha256:rollback')
		`, teamID, documentID, profileID, sourceID, contractID)
		return err
	}))

	err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
	require.ErrorContains(t, err, "active-contract search documents are not current")
	var attemptCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM remember_attempts
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&attemptCount))
	require.Zero(t, attemptCount)
	require.True(t, tableExists(t, ctx, db, "placement_runs"))
	var markerCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM v2_compatibility_markers
		WHERE marker_kind = 'v2_cutover'
		  AND version = 'dense-mem.v2.6.1.cutover.v1'
	`).Scan(&markerCount))
	require.Zero(t, markerCount)
}

func TestRememberSynchronousCutoverPreservesConflictDerivedPlacementHistory(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, db, rememberReliabilityMigrationVersion)
	teamID, profileID := insertRememberReliabilityIdentityFixture(t, ctx, db)
	ingestID, runID, itemID, fragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	conflictID, positionID := uuid.New(), uuid.New()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key,
				request_hash, source_summary, status, proposal, metadata, completed_at
			) VALUES ($1::uuid, $2::uuid, $3::uuid, 'conflict-derived-history',
			          'conflict-derived-history-request', 'overdue conflict deletion-only derivation',
			          'completed', '{}'::jsonb,
			          jsonb_build_object(
			              'contract_version', 'dense-mem.v2.6',
			              'conflict_id', $4::text,
			              'target_fragment_id', $5::text,
			              'selected_position_id', $6::text,
			              'conflict_resolution_deletion_only', true
			          ), now())
		`, teamID, ingestID, profileID, conflictID, fragmentID, positionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id,
				evidence_index, content, content_hash, source_type, authority, metadata
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          0, 'historical conflict deletion-only evidence', 'sha256:conflict-derived',
			          'observation', 'inferred',
			          jsonb_build_object(
			              'conflict_id', $5::text,
			              'target_fragment_id', $6::text,
			              'conflict_resolution_deletion_only', true
			          ))
		`, teamID, fragmentID, ingestID, profileID, conflictID, fragmentID); err != nil {
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
		_, err := tx.ExecContext(ctx, `
			INSERT INTO placement_items (
				team_id, placement_item_id, placement_run_id, ingest_id,
				owner_profile_id, fragment_id, evidence_index, claim_key,
				status, category, result
			) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			          $5::uuid, $6::uuid, 0, $7::uuid,
			          'completed', 'fragment_only', '{}'::jsonb)
		`, teamID, itemID, runID, ingestID, profileID, fragmentID, uuid.New())
		return err
	}))

	runGooseUpTo(t, ctx, db, synchronousRememberCutoverMigrationVersion)

	var outcome, origin string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT attempt.outcome, attempt.public_result ->> 'legacy_origin'
		FROM remember_attempts AS attempt
		WHERE attempt.team_id = $1::uuid AND attempt.attempt_id = $2::uuid
	`, teamID, ingestID).Scan(&outcome, &origin))
	require.Equal(t, "completed", outcome)
	require.Equal(t, "conflict_derived", origin)

	var itemEvents int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM remember_attempt_events
		WHERE team_id = $1::uuid AND attempt_id = $2::uuid AND event_kind = 'legacy_item'
	`, teamID, ingestID).Scan(&itemEvents))
	require.Equal(t, 1, itemEvents)
	require.False(t, tableExists(t, ctx, db, "placement_runs"))
	require.False(t, tableExists(t, ctx, db, "placement_items"))
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

	runGooseUpTo(t, ctx, db, synchronousRememberCutoverMigrationVersion)
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

			err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
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

			err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
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

			err := migrationUpTo(ctx, db, synchronousRememberCutoverMigrationVersion)
			require.Error(t, err)
			require.Contains(t, err.Error(), "active-contract search documents are not current")
			require.True(t, tableExists(t, ctx, db, "search_documents"))
		})
	}
}
