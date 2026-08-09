//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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

func TestEmbeddingReconciliationMigrationsPreserveRecoveryAndOrdering(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()

	runGooseUpTo(t, ctx, sqlDB, 2026080803)
	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	contractID := uuid.NewString()
	fixtures := []legacyEmbeddingFailureFixture{
		{jobID: uuid.NewString(), errorMessage: "embedding request timed out: openai: request timed out", wantClass: "transient", wantCode: "provider_timeout"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider error: openai: request failed: connection reset by peer", wantClass: "transient", wantCode: "provider_network_error"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider http error: status=503 message=unavailable", wantClass: "transient", wantCode: "provider_server_error"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider http error: status=429 message=rate limit exceeded", wantClass: "transient", wantCode: "provider_rate_limited"},
		{jobID: uuid.NewString(), errorMessage: "embedding provider http error: status=429 code=insufficient_quota", wantClass: "provider_action_required", wantCode: "provider_quota_exhausted"},
		{jobID: uuid.NewString(), errorMessage: "embedding dimensions 2, expected 3", wantClass: "permanent", wantCode: "unknown_embedding_failure"},
	}

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)`, teamID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)`, teamID, profileID); err != nil {
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
					$6::uuid, 3, 'failed', 20, 20, $7, now()
				)
			`, teamID, fixtures[index].jobID, documentID, profileID, sourceID,
				contractID, fixtures[index].errorMessage); err != nil {
				return err
			}
		}
		return nil
	}))

	runGooseUpTo(t, ctx, sqlDB, 2026080904)
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
			require.Equal(t, 20, totalAttempts, fixture.errorMessage)
			require.True(t, timestampsPresent, fixture.errorMessage)
			expectedIncidents[fixture.wantClass+"|"+fixture.wantCode]++
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT failure_class, failure_code, affected_job_count
			FROM embedding_failure_incidents
			WHERE team_id = $1::uuid AND status = 'open'
		`, teamID)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[string]int64{}
		for rows.Next() {
			var failureClass, failureCode string
			var affected int64
			if err := rows.Scan(&failureClass, &failureCode, &affected); err != nil {
				return err
			}
			seen[failureClass+"|"+failureCode] = affected
		}
		require.NoError(t, rows.Err())
		require.Equal(t, expectedIncidents, seen)
		return nil
	}))

	runGooseUpTo(t, ctx, sqlDB, 2026080905)
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

	runGooseUpTo(t, ctx, sqlDB, 2026080907)
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
}
