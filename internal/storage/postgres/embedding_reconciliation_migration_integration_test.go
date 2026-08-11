//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type legacyEmbeddingFailureFixture struct {
	jobID        string
	errorMessage string
	wantClass    string
	wantCode     string
}

func TestEmbeddingReconciliationBackfillDoesNotSkipLockedCandidates(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(getMigrationsDir(), "2026080905_embedding_reconciliation.sql"))
	require.NoError(t, err)
	start := strings.Index(string(body), "CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_compatibility()")
	end := strings.Index(string(body), "CALL dense_mem_backfill_embedding_reconciliation_compatibility()")
	require.NotEqual(t, -1, start)
	require.Greater(t, end, start)
	backfill := string(body)[start:end]
	require.Contains(t, backfill, "FOR UPDATE")
	require.NotContains(t, backfill, "SKIP LOCKED")
}

func TestEmbeddingReconciliationSupersededRetirementIsBatched(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(getMigrationsDir(), "2026080905_embedding_reconciliation.sql"))
	require.NoError(t, err)
	start := strings.Index(string(body), "CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080905()")
	end := strings.Index(string(body), "CALL dense_mem_backfill_embedding_reconciliation_2026080905()")
	require.NotEqual(t, -1, start)
	require.Greater(t, end, start)
	procedure := string(body)[start:end]
	retirementStart := strings.Index(procedure, "superseded_batch AS MATERIALIZED")
	require.NotEqual(t, -1, retirementStart)
	retirement := procedure[retirementStart:]
	require.Contains(t, retirement, "LIMIT 1000")
	require.Contains(t, retirement, "COMMIT;")
	require.Equal(t, 1, strings.Count(string(body), "SET status = 'stale'"))
}

func TestEmbeddingReconciliationMigrationsPreserveRecoveryAndOrdering(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026080803)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	teamBID, profileBID := insertMigrationTeamProfile(t, ctx, sqlDB)
	contractID := uuid.NewString()
	fixtures := []legacyEmbeddingFailureFixture{
		{jobID: uuid.NewString(), errorMessage: "embedding request timed out: openai: request timed out", wantClass: "transient", wantCode: "provider_timeout"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider error: openai: request failed: connection reset by peer", wantClass: "transient", wantCode: "provider_network_error"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider http error: status=503 message=unavailable", wantClass: "transient", wantCode: "provider_server_error"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider http error: status=429 message=rate limit exceeded", wantClass: "transient", wantCode: "provider_rate_limited"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider http error: status=429 code=insufficient_quota", wantClass: "provider_action_required", wantCode: "provider_quota_exhausted"},
		{jobID: uuid.NewString(), errorMessage: "embedding dimensions 2, expected 3", wantClass: "permanent", wantCode: "unknown_embedding_failure"},
	}
	teamBFixture := legacyEmbeddingFailureFixture{
		jobID: uuid.NewString(), errorMessage: "embedding request timed out: secondary team", wantClass: "transient", wantCode: "provider_timeout",
	}
	supersededJobID := uuid.NewString()
	supersededDocumentID := uuid.NewString()
	supersededSourceID := uuid.NewString()
	supersededError := "embedding provider http error: status=503 superseded"
	const supersededBatchSize = 1001

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)`, teamID, profileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)`, teamBID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)`, teamBID, profileBID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_contracts (
				embedding_contract_id, contract_key, version, provider, model,
				dimensions, distance_metric, vector_normalization,
				document_format_version, query_format_version, lifecycle_state
			) VALUES (
				$1::uuid, $2, 1, 'openai', 'legacy-model',
				3, 'cosine', 'provider', 1, 1, 'active'
			)
		`, contractID, "embedding-reconciliation-"+contractID); err != nil {
			return err
		}
		for index := range fixtures {
			documentID := uuid.NewString()
			sourceID := uuid.NewString()
			attempts := 20
			if index == 0 {
				attempts = 19
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO search_documents (
					team_id, search_document_id, owner_profile_id, source_kind, source_id,
					source_version, document_version, embedding_contract_id,
					embedding_dimensions, search_state, document_text, document_hash,
					embedding_error
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, 'evidence', $4::uuid,
					1, 1, $5::uuid, 3, 'failed', $6, $7, $8
				)
			`, teamID, documentID, profileID, sourceID, contractID,
				"legacy failure "+fixtures[index].jobID,
				"sha256:"+strings.ReplaceAll(fixtures[index].jobID, "-", ""),
				fixtures[index].errorMessage); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO embedding_jobs (
					team_id, embedding_job_id, search_document_id, owner_profile_id,
					source_kind, source_id, source_version, document_version,
					embedding_contract_id, embedding_dimensions, status, attempts,
					max_attempts, error, completed_at
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4::uuid,
					'evidence', $5::uuid, 1, 1,
					$6::uuid, 3, 'failed', $7, 20, $8, now()
				)
			`, teamID, fixtures[index].jobID, documentID, profileID, sourceID,
				contractID, attempts, fixtures[index].errorMessage); err != nil {
				return err
			}
		}
		teamBDocumentID := uuid.NewString()
		teamBSourceID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash,
				embedding_error
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'evidence', $4::uuid,
				1, 1, $5::uuid, 3, 'failed', $6, $7, $8
			)
		`, teamBID, teamBDocumentID, profileBID, teamBSourceID, contractID,
			"legacy failure "+teamBFixture.jobID,
			"sha256:"+strings.ReplaceAll(teamBFixture.jobID, "-", ""),
			teamBFixture.errorMessage); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, error, completed_at
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'evidence', $5::uuid, 1, 1,
				$6::uuid, 3, 'failed', 20, 20, $7, now()
			)
		`, teamBID, teamBFixture.jobID, teamBDocumentID, profileBID, teamBSourceID,
			contractID, teamBFixture.errorMessage); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'relationship', $4::uuid,
				1, 2, $5::uuid, 3, 'current', 'superseding relationship document', $6
			)
		`, teamID, supersededDocumentID, profileID, supersededSourceID, contractID,
			"sha256:"+strings.ReplaceAll(supersededDocumentID, "-", "")); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, error, completed_at
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'relationship', $5::uuid, 1, 1, $6::uuid, 3,
				'failed', 20, 20, $7, now()
			)
		`, teamID, supersededJobID, supersededDocumentID, profileID, supersededSourceID, contractID, supersededError); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, error, completed_at
			)
			SELECT $1::uuid, gen_random_uuid(), $2::uuid, $3::uuid,
			       'relationship', gen_random_uuid(), 1, 1, $4::uuid, 3,
			       'failed', 20, 20, $5, now()
			FROM generate_series(1, $6)
		`, teamID, supersededDocumentID, profileID, contractID, supersededError, supersededBatchSize)
		return err
	}))

	legacyBackfillQueuedJobID := uuid.NewString()
	legacyBackfillQueuedDocumentID := uuid.NewString()
	legacyBackfillQueuedSourceID := uuid.NewString()
	healthyBackfillQueuedJobID := uuid.NewString()
	healthyBackfillQueuedDocumentID := uuid.NewString()
	healthyBackfillQueuedSourceID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		for _, fixture := range []struct {
			jobID, documentID, sourceID, text, hash, errorMessage string
		}{
			{legacyBackfillQueuedJobID, legacyBackfillQueuedDocumentID, legacyBackfillQueuedSourceID, "legacy queued backfill", "sha256:" + strings.ReplaceAll(legacyBackfillQueuedDocumentID, "-", ""), "embedding provider error: connection reset by peer"},
			{healthyBackfillQueuedJobID, healthyBackfillQueuedDocumentID, healthyBackfillQueuedSourceID, "healthy queued backfill", "sha256:" + strings.ReplaceAll(healthyBackfillQueuedDocumentID, "-", ""), ""},
		} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO search_documents (
					team_id, search_document_id, owner_profile_id, source_kind, source_id,
					source_version, document_version, embedding_contract_id,
					embedding_dimensions, search_state, document_text, document_hash
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, 'relationship', $4::uuid,
					1, 1, $5::uuid, 3, 'pending', $6, $7
				)
			`, teamID, fixture.documentID, profileID, fixture.sourceID, contractID, fixture.text, fixture.hash); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO embedding_jobs (
					team_id, embedding_job_id, search_document_id, owner_profile_id,
					source_kind, source_id, source_version, document_version,
					embedding_contract_id, embedding_dimensions, status, attempts,
					max_attempts, error
				) VALUES (
					$1::uuid, $2::uuid, $3::uuid, $4::uuid,
					'relationship', $5::uuid, 1, 1, $6::uuid, 3,
					'queued', 1, 20, $7
				)
			`, teamID, fixture.jobID, fixture.documentID, profileID, fixture.sourceID, contractID, fixture.errorMessage); err != nil {
				return err
			}
		}
		return nil
	}))

	runGooseUpTo(t, ctx, sqlDB, 2026080905)
	var compatibilityTotalAttempts int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			UPDATE embedding_jobs
			SET attempts = attempts + 1
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
			RETURNING total_attempts
		`, teamID, fixtures[0].jobID).Scan(&compatibilityTotalAttempts)
	}))
	require.Equal(t, 20, compatibilityTotalAttempts, "legacy claim updates must preserve total_attempts")
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE embedding_jobs
			SET attempts = 0
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, fixtures[0].jobID); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			UPDATE embedding_jobs
			SET attempts = attempts + 1
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
			RETURNING total_attempts
		`, teamID, fixtures[0].jobID).Scan(&compatibilityTotalAttempts)
	}))
	require.Equal(t, 21, compatibilityTotalAttempts, "legacy claims after recovery reset must increment lifetime attempts")
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		expectedIncidents := make(map[string]int64, len(fixtures))
		for _, fixture := range fixtures {
			var failureClass, failureCode string
			var totalAttempts int
			var timestampsPresent bool
			if err := tx.QueryRowContext(ctx, `
				SELECT failure_class, failure_code, total_attempts,
				       first_failed_at IS NOT NULL AND last_failed_at IS NOT NULL
				FROM embedding_jobs
				WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
			`, teamID, fixture.jobID).Scan(
				&failureClass, &failureCode, &totalAttempts, &timestampsPresent,
			); err != nil {
				return err
			}
			require.Equal(t, fixture.wantClass, failureClass, fixture.errorMessage)
			require.Equal(t, fixture.wantCode, failureCode, fixture.errorMessage)
			wantTotalAttempts := 20
			if fixture.jobID == fixtures[0].jobID {
				wantTotalAttempts = compatibilityTotalAttempts
			}
			require.Equal(t, wantTotalAttempts, totalAttempts, fixture.errorMessage)
			require.True(t, timestampsPresent, fixture.errorMessage)
			expectedIncidents[fixture.wantClass+"|"+fixture.wantCode]++
		}
		for _, scoped := range []struct {
			teamID   string
			expected map[string]int64
		}{
			{teamID: teamID, expected: expectedIncidents},
			{teamID: teamBID, expected: map[string]int64{teamBFixture.wantClass + "|" + teamBFixture.wantCode: 1}},
		} {
			rows, err := tx.QueryContext(ctx, `
				SELECT failure_class, failure_code, affected_job_count
				FROM embedding_failure_incidents
				WHERE team_id = $1::uuid AND status = 'open'
			`, scoped.teamID)
			if err != nil {
				return err
			}
			seen := map[string]int64{}
			for rows.Next() {
				var failureClass, failureCode string
				var affected int64
				if err := rows.Scan(&failureClass, &failureCode, &affected); err != nil {
					_ = rows.Close()
					return err
				}
				seen[failureClass+"|"+failureCode] = affected
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			require.Equal(t, scoped.expected, seen, "incident aggregation for team %s", scoped.teamID)
		}
		return nil
	}))
	var backfillFailureClass, backfillFailureCode, backfillSearchState string
	var healthyBackfillFailureClass, healthyBackfillFailureCode string
	var healthyBackfillFirstFailedAtIsNull bool
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT job.failure_class, job.failure_code, document.search_state
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id AND document.search_document_id = job.search_document_id
			WHERE job.team_id = $1::uuid AND job.embedding_job_id = $2::uuid
		`, teamID, legacyBackfillQueuedJobID).Scan(&backfillFailureClass, &backfillFailureCode, &backfillSearchState); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT failure_class, failure_code, first_failed_at IS NULL
			FROM embedding_jobs
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, healthyBackfillQueuedJobID).Scan(&healthyBackfillFailureClass, &healthyBackfillFailureCode, &healthyBackfillFirstFailedAtIsNull)
	}))
	require.Equal(t, "transient", backfillFailureClass)
	require.Equal(t, "provider_network_error", backfillFailureCode)
	require.Equal(t, "pending", backfillSearchState)
	require.Equal(t, "permanent", healthyBackfillFailureClass)
	require.Equal(t, "unknown_embedding_failure", healthyBackfillFailureCode)
	require.True(t, healthyBackfillFirstFailedAtIsNull)

	var supersededStatus, supersededJobError string
	var supersededIncidentCount, supersededRetiredCount int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT status, error
			FROM embedding_jobs
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, supersededJobID).Scan(&supersededStatus, &supersededJobError); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM embedding_jobs
			WHERE team_id = $1::uuid
			  AND search_document_id = $2::uuid
			  AND status = 'stale'
			  AND error = 'superseded by newer document version'
		`, teamID, supersededDocumentID).Scan(&supersededRetiredCount); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM embedding_failure_incidents
			WHERE team_id = $1::uuid
			  AND embedding_contract_id = $2::uuid
			  AND embedding_dimensions = 3
			  AND source_kind = 'relationship'
			  AND failure_class = 'transient'
			  AND failure_code = 'provider_server_error'
		`, teamID, contractID).Scan(&supersededIncidentCount)
	}))
	require.Equal(t, "stale", supersededStatus)
	require.Equal(t, "superseded by newer document version", supersededJobError)
	require.Equal(t, supersededBatchSize+1, supersededRetiredCount)
	require.Zero(t, supersededIncidentCount)

	var plan strings.Builder
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			EXPLAIN (COSTS OFF)
			SELECT embedding_job_id
			FROM embedding_jobs
			WHERE status = 'failed'
			  AND failure_class <> 'permanent'
			  AND embedding_contract_id = $1::uuid
			  AND embedding_dimensions = 3
			  AND COALESCE(last_failed_at, updated_at) <= now()
			ORDER BY COALESCE(last_failed_at, updated_at), embedding_job_id
			LIMIT 500
		`, contractID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		return rows.Err()
	}))
	require.Contains(t, plan.String(), "embedding_jobs_reconciliation_failed_idx")
	require.NotContains(t, plan.String(), "Sort")

	var validated int
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM pg_constraint
			WHERE conrelid = 'embedding_jobs'::regclass
			  AND conname = ANY($1::text[])
			  AND convalidated
		`, pq.Array([]string{
			"embedding_jobs_total_attempts_check",
			"embedding_jobs_recovery_count_check",
			"embedding_jobs_failure_class_check",
			"embedding_jobs_failure_code_check",
		})).Scan(&validated)
	}))
	require.Equal(t, 4, validated)

	legacyPreQueuedJobID := uuid.NewString()
	legacyPreQueuedDocumentID := uuid.NewString()
	legacyPreQueuedSourceID := uuid.NewString()
	legacyPreQueuedError := "embedding provider error: connection reset by peer"
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'relationship', $4::uuid,
				1, 1, $5::uuid, 3, 'pending', 'legacy queued before compatibility', $6
			)
		`, teamID, legacyPreQueuedDocumentID, profileID, legacyPreQueuedSourceID, contractID,
			"sha256:"+strings.ReplaceAll(legacyPreQueuedDocumentID, "-", "")); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, error
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'relationship', $5::uuid, 1, 1, $6::uuid, 3,
				'queued', 1, 20, $7
			)
		`, teamID, legacyPreQueuedJobID, legacyPreQueuedDocumentID, profileID, legacyPreQueuedSourceID, contractID, legacyPreQueuedError)
		return err
	}))
	healthyQueuedJobID := uuid.NewString()
	healthyQueuedDocumentID := uuid.NewString()
	healthyQueuedSourceID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'relationship', $4::uuid,
				1, 1, $5::uuid, 3, 'pending', 'healthy queued document', $6
			)
		`, teamID, healthyQueuedDocumentID, profileID, healthyQueuedSourceID, contractID,
			"sha256:"+strings.ReplaceAll(healthyQueuedDocumentID, "-", "")); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, error
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'relationship', $5::uuid, 1, 1, $6::uuid, 3,
				'queued', 0, 20, ''
			)
		`, teamID, healthyQueuedJobID, healthyQueuedDocumentID, profileID, healthyQueuedSourceID, contractID)
		return err
	}))

	legacyEntityJobID := uuid.NewString()
	legacyEntityDocumentID := uuid.NewString()
	legacyEntityID := uuid.NewString()
	legacyError := "embedding provider error: connection reset by peer"
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'entity', $4::uuid,
				1, 1, $5::uuid, 3, 'pending', 'legacy entity document', $6
			)
		`, teamID, legacyEntityDocumentID, profileID, legacyEntityID, contractID,
			"sha256:"+strings.ReplaceAll(legacyEntityDocumentID, "-", "")); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, worker_id, lease_until, error
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'entity', $5::uuid, 1, 1, $6::uuid, 3,
				'processing', 1, 5, 'legacy-worker', now() + interval '1 minute', ''
			)
		`, teamID, legacyEntityJobID, legacyEntityDocumentID, profileID, legacyEntityID, contractID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE embedding_jobs
			SET status = 'failed', error = $1, worker_id = '', lease_until = NULL, completed_at = now()
			WHERE team_id = $2::uuid AND embedding_job_id = $3::uuid
		`, legacyError, teamID, legacyEntityJobID)
		return err
	}))

	var failureClass, failureCode, searchState string
	var timestampsPresent bool
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT job.failure_class, job.failure_code,
			       job.first_failed_at IS NOT NULL AND job.last_failed_at IS NOT NULL,
			       document.search_state
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id AND document.search_document_id = job.search_document_id
			WHERE job.team_id = $1::uuid AND job.embedding_job_id = $2::uuid
		`, teamID, legacyEntityJobID).Scan(&failureClass, &failureCode, &timestampsPresent, &searchState)
	}))
	require.Equal(t, "transient", failureClass)
	require.Equal(t, "provider_network_error", failureCode)
	require.True(t, timestampsPresent)
	require.Equal(t, "failed", searchState)

	var queuedFailureClass, queuedFailureCode, queuedSearchState string
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT job.failure_class, job.failure_code, document.search_state
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id AND document.search_document_id = job.search_document_id
			WHERE job.team_id = $1::uuid AND job.embedding_job_id = $2::uuid
		`, teamID, legacyPreQueuedJobID).Scan(&queuedFailureClass, &queuedFailureCode, &queuedSearchState)
	}))
	require.Equal(t, "transient", queuedFailureClass)
	require.Equal(t, "provider_network_error", queuedFailureCode)
	require.Equal(t, "pending", queuedSearchState)

	var incidentStatus string
	var affectedJobs int64
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, affected_job_count
			FROM embedding_failure_incidents
			WHERE team_id = $1::uuid
			  AND embedding_contract_id = $2::uuid
			  AND embedding_dimensions = 3
			  AND source_kind = 'entity'
			  AND failure_class = 'transient'
			  AND failure_code = 'provider_network_error'
		`, teamID, contractID).Scan(&incidentStatus, &affectedJobs)
	}))
	require.Equal(t, "open", incidentStatus)
	require.EqualValues(t, 1, affectedJobs)
	legacyFailureTimestamp := time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE embedding_jobs
			SET last_failed_at = $1
			WHERE team_id = $2::uuid AND embedding_job_id = $3::uuid
		`, legacyFailureTimestamp, teamID, legacyEntityJobID)
		return err
	}))

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE embedding_jobs
			SET status = 'failed', error = 'embedding provider http error: status=401', completed_at = now()
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, legacyEntityJobID)
		return err
	}))
	var lastFailedAt time.Time
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT failure_class, failure_code, last_failed_at
			FROM embedding_jobs
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, legacyEntityJobID).Scan(&failureClass, &failureCode, &lastFailedAt)
	}))
	require.Equal(t, "provider_action_required", failureClass)
	require.Equal(t, "provider_authentication_failed", failureCode)
	require.True(t, lastFailedAt.After(legacyFailureTimestamp))
	explicitFailureTimestamp := time.Date(2002, time.January, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.embedding_job_failure_writer', 'current', true)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE embedding_jobs
			SET status = 'failed', error = 'embedding provider http error: status=401',
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    last_failed_at = $1, completed_at = now()
			WHERE team_id = $2::uuid AND embedding_job_id = $3::uuid
		`, explicitFailureTimestamp, teamID, legacyEntityJobID)
		return err
	}))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT failure_class, failure_code, last_failed_at
			FROM embedding_jobs
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, legacyEntityJobID).Scan(&failureClass, &failureCode, &lastFailedAt)
	}))
	require.Equal(t, "transient", failureClass)
	require.Equal(t, "provider_timeout", failureCode)
	require.True(t, explicitFailureTimestamp.Equal(lastFailedAt))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, affected_job_count
			FROM embedding_failure_incidents
			WHERE team_id = $1::uuid
			  AND embedding_contract_id = $2::uuid
			  AND embedding_dimensions = 3
			  AND source_kind = 'entity'
			  AND failure_class = 'transient'
			  AND failure_code = 'provider_network_error'
		`, teamID, contractID).Scan(&incidentStatus, &affectedJobs)
	}))
	require.Equal(t, "resolved", incidentStatus)
	require.Zero(t, affectedJobs)

	completionEntityJobID := uuid.NewString()
	completionEntityDocumentID := uuid.NewString()
	completionEntityID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash,
				embedding_error
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'entity', $4::uuid,
				1, 1, $5::uuid, 3, 'failed', 'completion entity document', $6, $7
			)
		`, teamID, completionEntityDocumentID, profileID, completionEntityID, contractID,
			"sha256:"+strings.ReplaceAll(completionEntityDocumentID, "-", ""), legacyError); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, error, completed_at, failure_class, failure_code,
				first_failed_at, last_failed_at
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'entity', $5::uuid, 1, 1, $6::uuid, 3, 'failed', 20, 20,
				$7, now(), 'transient', 'provider_network_error', now(), now()
			)
		`, teamID, completionEntityJobID, completionEntityDocumentID, profileID, completionEntityID, contractID, legacyError)
		return err
	}))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, affected_job_count
			FROM embedding_failure_incidents
			WHERE team_id = $1::uuid
			  AND embedding_contract_id = $2::uuid
			  AND embedding_dimensions = 3
			  AND source_kind = 'entity'
			  AND failure_class = 'transient'
			  AND failure_code = 'provider_network_error'
		`, teamID, contractID).Scan(&incidentStatus, &affectedJobs)
	}))
	require.Equal(t, "open", incidentStatus)
	require.EqualValues(t, 1, affectedJobs)

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE embedding_jobs
			SET status = 'completed', error = '', completed_at = now()
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, completionEntityJobID)
		return err
	}))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, affected_job_count
			FROM embedding_failure_incidents
			WHERE team_id = $1::uuid
			  AND embedding_contract_id = $2::uuid
			  AND embedding_dimensions = 3
			  AND source_kind = 'entity'
			  AND failure_class = 'transient'
			  AND failure_code = 'provider_network_error'
		`, teamID, contractID).Scan(&incidentStatus, &affectedJobs)
	}))
	require.Equal(t, "resolved", incidentStatus)
	require.Zero(t, affectedJobs)

	var embeddingErrorLength int
	var repeatedFailureClass, repeatedFailureCode string
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE embedding_jobs
			SET status = 'failed', error = repeat('x', 2048), completed_at = now()
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, legacyEntityJobID)
		return err
	}))
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT length(document.embedding_error)
			FROM search_documents AS document
			WHERE document.team_id = $1::uuid AND document.search_document_id = $2::uuid
		`, teamID, legacyEntityDocumentID).Scan(&embeddingErrorLength)
	}))
	require.Equal(t, 1024, embeddingErrorLength)
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT failure_class, failure_code
			FROM embedding_jobs
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, legacyEntityJobID).Scan(&repeatedFailureClass, &repeatedFailureCode)
	}))
	require.Equal(t, "permanent", repeatedFailureClass)
	require.Equal(t, "unknown_embedding_failure", repeatedFailureCode)

	legacyQueuedJobID := uuid.NewString()
	legacyQueuedDocumentID := uuid.NewString()
	legacyQueuedSourceID := uuid.NewString()
	legacyQueuedError := "embedding provider error: connection reset by peer"
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO search_documents (
				team_id, search_document_id, owner_profile_id, source_kind, source_id,
				source_version, document_version, embedding_contract_id,
				embedding_dimensions, search_state, document_text, document_hash
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, 'entity', $4::uuid,
				1, 1, $5::uuid, 3, 'pending', 'legacy queued document', $6
			)
		`, teamID, legacyQueuedDocumentID, profileID, legacyQueuedSourceID, contractID,
			"sha256:"+strings.ReplaceAll(legacyQueuedDocumentID, "-", "")); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				max_attempts, error
			) VALUES (
				$1::uuid, $2::uuid, $3::uuid, $4::uuid,
				'entity', $5::uuid, 1, 1, $6::uuid, 3,
				'queued', 1, 20, $7
			)
		`, teamID, legacyQueuedJobID, legacyQueuedDocumentID, profileID, legacyQueuedSourceID, contractID, legacyQueuedError)
		return err
	}))

	var postQueuedFailureClass, postQueuedFailureCode, postQueuedSearchState string
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT job.failure_class, job.failure_code, document.search_state
			FROM embedding_jobs AS job
			JOIN search_documents AS document
			  ON document.team_id = job.team_id AND document.search_document_id = job.search_document_id
			WHERE job.team_id = $1::uuid AND job.embedding_job_id = $2::uuid
		`, teamID, legacyQueuedJobID).Scan(&postQueuedFailureClass, &postQueuedFailureCode, &postQueuedSearchState)
	}))
	require.Equal(t, "transient", postQueuedFailureClass)
	require.Equal(t, "provider_network_error", postQueuedFailureCode)
	require.Equal(t, "pending", postQueuedSearchState)

	var healthyFailureClass, healthyFailureCode string
	var healthyFirstFailedAtIsNull bool
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "migration", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT failure_class, failure_code, first_failed_at IS NULL
			FROM embedding_jobs
			WHERE team_id = $1::uuid AND embedding_job_id = $2::uuid
		`, teamID, healthyQueuedJobID).Scan(&healthyFailureClass, &healthyFailureCode, &healthyFirstFailedAtIsNull)
	}))
	require.Equal(t, "permanent", healthyFailureClass)
	require.Equal(t, "unknown_embedding_failure", healthyFailureCode)
	require.True(t, healthyFirstFailedAtIsNull)
}
