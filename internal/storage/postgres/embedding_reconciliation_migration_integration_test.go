//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type legacyEmbeddingFailureFixture struct {
	jobID              string
	documentID         string
	sourceID           string
	status             string
	errorMessage       string
	attempts           int
	documentVersion    int
	jobDocumentVersion int
}

func TestEmbeddingReconciliationMigrationHasOneRestartGatedBackfill(t *testing.T) {
	migrationFile, err := migrationPath(getMigrationsDir(), 2026080905)
	require.NoError(t, err)
	body, err := os.ReadFile(migrationFile)
	require.NoError(t, err)
	migration := string(body)

	require.Contains(t, migration, "requires a coordinated application restart")
	require.Equal(t, 1, strings.Count(migration, "CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080905()"))
	require.Contains(t, migration, "FOR UPDATE")
	require.NotContains(t, migration, "SKIP LOCKED")
	require.GreaterOrEqual(t, strings.Count(migration, "LIMIT 1000"), 3)
	require.GreaterOrEqual(t, strings.Count(migration, "COMMIT;"), 3)
	require.Equal(t, 3, strings.Count(migration, "LOOP\n        PERFORM set_config('app.tx_mode', 'migration', true);"))
	require.NotContains(t, migration, "EXIT WHEN updated_rows = 0;\n        PERFORM set_config")
	require.NotContains(t, migration, "embedding_failure_incidents")
	require.NotContains(t, migration, "CREATE TRIGGER")
	require.NotContains(t, migration, "pg_advisory")
	require.Contains(t, migration, "embedding_jobs_failure_groups_idx")
	require.Contains(t, migration, "INCLUDE (first_failed_at, last_failed_at)")
}

func TestEmbeddingReconciliationMigrationBackfillsJobsWithoutIncidentState(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026080803)

	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	contractID := uuid.NewString()
	failed := legacyEmbeddingFailureFixture{
		jobID: uuid.NewString(), documentID: uuid.NewString(), sourceID: uuid.NewString(),
		status: "failed", errorMessage: "embedding request timed out: provider response", attempts: 20,
	}
	queuedError := legacyEmbeddingFailureFixture{
		jobID: uuid.NewString(), documentID: uuid.NewString(), sourceID: uuid.NewString(),
		status: "queued", errorMessage: "embedding provider error: connection reset by peer", attempts: 1,
	}
	healthyQueued := legacyEmbeddingFailureFixture{
		jobID: uuid.NewString(), documentID: uuid.NewString(), sourceID: uuid.NewString(), status: "queued",
	}
	superseded := legacyEmbeddingFailureFixture{
		jobID: uuid.NewString(), documentID: uuid.NewString(), sourceID: uuid.NewString(),
		status: "failed", errorMessage: "embedding provider http error: status=503", attempts: 20,
		documentVersion: 2, jobDocumentVersion: 1,
	}

	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO semantic_team_refs (team_id) VALUES ($1::uuid)`, []any{teamID}},
			{`INSERT INTO semantic_profile_refs (team_id, profile_id) VALUES ($1::uuid, $2::uuid)`, []any{teamID, profileID}},
			{`
				INSERT INTO embedding_contracts (
					embedding_contract_id, contract_key, version, provider, model,
					dimensions, distance_metric, vector_normalization,
					document_format_version, query_format_version, lifecycle_state
				) VALUES (
					$1::uuid, $2, 1, 'openai', 'migration-model', 3,
					'cosine', 'provider', 1, 1, 'active'
				)
			`, []any{contractID, "embedding-reconciliation-" + contractID}},
		} {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		for _, fixture := range []legacyEmbeddingFailureFixture{failed, queuedError, healthyQueued, superseded} {
			if err := insertLegacyEmbeddingFailureFixture(ctx, tx, teamID, profileID, contractID, fixture); err != nil {
				return err
			}
		}
		return nil
	}))

	runGooseUpTo(t, ctx, sqlDB, 2026080905)

	assertMigratedEmbeddingJob(t, ctx, sqlDB, failed.jobID, migratedEmbeddingJobExpectation{
		status: "failed", totalAttempts: 20, failureClass: "transient", failureCode: "provider_timeout", failedTimestamps: true,
	})
	assertMigratedEmbeddingJob(t, ctx, sqlDB, queuedError.jobID, migratedEmbeddingJobExpectation{
		status: "queued", totalAttempts: 1, failureClass: "transient", failureCode: "provider_network_error", failedTimestamps: true,
	})
	assertMigratedEmbeddingJob(t, ctx, sqlDB, healthyQueued.jobID, migratedEmbeddingJobExpectation{
		status: "queued", totalAttempts: 0, failureClass: "permanent", failureCode: "unknown_embedding_failure", failedTimestamps: false,
	})
	assertMigratedEmbeddingJob(t, ctx, sqlDB, superseded.jobID, migratedEmbeddingJobExpectation{
		status: "stale", totalAttempts: 20, failureClass: "transient", failureCode: "provider_server_error", failedTimestamps: true,
	})

	var failedDocumentState, queuedDocumentState, healthyDocumentState string
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		for _, item := range []struct {
			documentID string
			state      *string
		}{
			{failed.documentID, &failedDocumentState},
			{queuedError.documentID, &queuedDocumentState},
			{healthyQueued.documentID, &healthyDocumentState},
		} {
			if err := tx.QueryRowContext(ctx, `
				SELECT search_state FROM search_documents WHERE search_document_id = $1::uuid
			`, item.documentID).Scan(item.state); err != nil {
				return err
			}
		}
		return nil
	}))
	require.Equal(t, "failed", failedDocumentState)
	require.Equal(t, "pending", queuedDocumentState)
	require.Equal(t, "pending", healthyDocumentState)

	var constraintsValidated int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conrelid = 'embedding_jobs'::regclass
		  AND convalidated
		  AND conname IN (
			'embedding_jobs_total_attempts_check',
			'embedding_jobs_recovery_count_check',
			'embedding_jobs_failure_class_check',
			'embedding_jobs_failure_code_check',
			'embedding_jobs_failure_contract_check'
		  )
	`).Scan(&constraintsValidated))
	require.Equal(t, 5, constraintsValidated)

	var rowSecurity, forceRowSecurity bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class
		WHERE oid = 'embedding_reconciliation_runs'::regclass
	`).Scan(&rowSecurity, &forceRowSecurity))
	require.True(t, rowSecurity)
	require.True(t, forceRowSecurity)
	var policyQual, policyCheck sql.NullString
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT qual, with_check
		FROM pg_policies
		WHERE schemaname = 'public'
		  AND tablename = 'embedding_reconciliation_runs'
		  AND policyname = 'embedding_reconciliation_runs_system_access'
	`).Scan(&policyQual, &policyCheck))
	require.Contains(t, policyQual.String, "system")
	require.Contains(t, policyQual.String, "migration")
	require.Contains(t, policyCheck.String, "system")
	require.Contains(t, policyCheck.String, "migration")

	runID := uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, sqlDB, "system", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO embedding_reconciliation_runs (
				reconciliation_run_id, embedding_contract_id, embedding_dimensions,
				local_run_date, status
			) VALUES ($1::uuid, $2::uuid, 3, CURRENT_DATE, 'completed')
		`, runID, contractID)
		return err
	}))
	roleName := "dense_mem_reconciliation_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := quoteMigrationIdentifier(roleName)
	if _, err := sqlDB.ExecContext(ctx, "CREATE ROLE "+quotedRole+" NOLOGIN NOSUPERUSER NOBYPASSRLS"); err != nil {
		if isPostgresInsufficientPrivilege(err) {
			t.Skipf("reconciliation RLS behavior test requires role administration: %v", err)
		}
		require.NoError(t, err)
	}
	defer func() {
		_, _ = sqlDB.ExecContext(ctx, "DROP OWNED BY "+quotedRole)
		_, _ = sqlDB.ExecContext(ctx, "DROP ROLE IF EXISTS "+quotedRole)
	}()
	_, err := sqlDB.ExecContext(ctx, "GRANT USAGE ON SCHEMA public TO "+quotedRole)
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx, "GRANT SELECT, INSERT, UPDATE ON embedding_reconciliation_runs TO "+quotedRole)
	require.NoError(t, err)
	requestTx, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer requestTx.Rollback()
	_, err = requestTx.ExecContext(ctx, "SET LOCAL ROLE "+quotedRole)
	require.NoError(t, err)
	for key, value := range map[string]string{
		"app.tx_mode": "team", "app.current_team_id": teamID, "app.current_profile_id": profileID,
	} {
		_, err = requestTx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, key, value)
		require.NoError(t, err)
	}
	var visibleRuns int
	require.NoError(t, requestTx.QueryRowContext(ctx, `
		SELECT count(*) FROM embedding_reconciliation_runs WHERE reconciliation_run_id = $1::uuid
	`, runID).Scan(&visibleRuns))
	require.Zero(t, visibleRuns)
	update, err := requestTx.ExecContext(ctx, `
		UPDATE embedding_reconciliation_runs SET status = 'failed' WHERE reconciliation_run_id = $1::uuid
	`, runID)
	require.NoError(t, err)
	updatedRows, err := update.RowsAffected()
	require.NoError(t, err)
	require.Zero(t, updatedRows)
	_, err = requestTx.ExecContext(ctx, `
		INSERT INTO embedding_reconciliation_runs (
			embedding_contract_id, embedding_dimensions, local_run_date, status
		) VALUES ($1::uuid, 3, CURRENT_DATE + 1, 'reserved')
	`, contractID)
	require.Error(t, err)

	var incidentTableAbsent bool
	require.NoError(t, sqlDB.QueryRowContext(ctx, `SELECT to_regclass('public.embedding_failure_incidents') IS NULL`).Scan(&incidentTableAbsent))
	require.True(t, incidentTableAbsent)
	var compatibilityObjects int
	require.NoError(t, sqlDB.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_trigger
		   WHERE tgrelid = 'embedding_jobs'::regclass
		     AND tgname IN ('embedding_jobs_failure_compatibility_before', 'embedding_jobs_failure_compatibility_after'))
		+ (SELECT count(*) FROM pg_proc
		   WHERE proname IN (
			'dense_mem_record_embedding_job_failure_compatibility',
			'dense_mem_classify_embedding_job_failure_compatibility',
			'dense_mem_classify_embedding_failure_compatibility'
		   ))
	`).Scan(&compatibilityObjects))
	require.Zero(t, compatibilityObjects)
}

func TestEmbeddingFailureGroupIndexSupportsRepresentativeProjection(t *testing.T) {
	ctx := context.Background()
	sqlDB, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, sqlDB, 2026080905)

	teamID, profileID := insertMigrationTeamProfile(t, ctx, sqlDB)
	contractID := uuid.NewString()
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
			) VALUES ($1::uuid, $2, 1, 'openai', 'planner-model', 3, 'cosine', 'provider', 1, 1, 'active')
		`, contractID, "embedding-reconciliation-planner-"+contractID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			WITH generated AS MATERIALIZED (
				SELECT gen_random_uuid() AS document_id,
				       gen_random_uuid() AS source_id,
				       ordinal
				FROM generate_series(1, 4200) AS ordinal
			), documents AS (
				INSERT INTO search_documents (
					team_id, search_document_id, owner_profile_id, source_kind, source_id,
					source_version, document_version, embedding_contract_id,
					embedding_dimensions, search_state, document_text, document_hash,
					embedding_error
				)
				SELECT $1::uuid, document_id, $2::uuid, 'evidence', source_id,
				       1, 1, $3::uuid, 3,
				       CASE WHEN ordinal <= 200 THEN 'failed' ELSE 'pending' END,
				       'failure group planner fixture ' || ordinal,
				       'sha256:' || replace(document_id::text, '-', ''),
				       CASE WHEN ordinal <= 200 THEN 'embedding provider timed out' ELSE '' END
				FROM generated
				RETURNING team_id, search_document_id, owner_profile_id, source_kind,
				          source_id, source_version, document_version,
				          embedding_contract_id, embedding_dimensions, search_state
			)
			INSERT INTO embedding_jobs (
				team_id, embedding_job_id, search_document_id, owner_profile_id,
				source_kind, source_id, source_version, document_version,
				embedding_contract_id, embedding_dimensions, status, attempts,
				total_attempts, max_attempts, error, completed_at,
				failure_class, failure_code, first_failed_at, last_failed_at
			)
			SELECT team_id, gen_random_uuid(), search_document_id, owner_profile_id,
			       source_kind, source_id, source_version, document_version,
			       embedding_contract_id, embedding_dimensions,
			       CASE WHEN search_state = 'failed' THEN 'failed' ELSE 'queued' END,
			       CASE WHEN search_state = 'failed' THEN 20 ELSE 0 END,
			       CASE WHEN search_state = 'failed' THEN 20 ELSE 0 END,
			       20,
			       CASE WHEN search_state = 'failed' THEN 'embedding provider timed out' ELSE '' END,
			       CASE WHEN search_state = 'failed' THEN now() ELSE NULL END,
			       CASE WHEN search_state = 'failed' THEN 'transient' ELSE 'permanent' END,
			       CASE WHEN search_state = 'failed' THEN 'provider_timeout' ELSE 'unknown_embedding_failure' END,
			       CASE WHEN search_state = 'failed' THEN now() ELSE NULL END,
			       CASE WHEN search_state = 'failed' THEN now() ELSE NULL END
			FROM documents
		`, teamID, profileID, contractID)
		return err
	}))
	require.NoError(t, func() error {
		_, err := sqlDB.ExecContext(ctx, `VACUUM (ANALYZE) embedding_jobs`)
		return err
	}())

	rows, err := sqlDB.QueryContext(ctx, `
		EXPLAIN (COSTS OFF)
		SELECT team_id, source_kind, failure_class, failure_code, status,
		       count(*), min(first_failed_at), max(last_failed_at)
		FROM embedding_jobs
		WHERE embedding_contract_id = $1::uuid
		  AND embedding_dimensions = 3
		  AND first_failed_at IS NOT NULL
		  AND status IN ('queued', 'processing', 'failed')
		GROUP BY team_id, source_kind, failure_class, failure_code, status
	`, contractID)
	require.NoError(t, err)
	defer rows.Close()
	var planLines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		planLines = append(planLines, line)
	}
	require.NoError(t, rows.Err())
	plan := strings.Join(planLines, "\n")
	require.Contains(t, plan, "Index Only Scan using embedding_jobs_failure_groups_idx", plan)
}

type migratedEmbeddingJobExpectation struct {
	status           string
	totalAttempts    int
	failureClass     string
	failureCode      string
	failedTimestamps bool
}

func assertMigratedEmbeddingJob(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	jobID string,
	want migratedEmbeddingJobExpectation,
) {
	t.Helper()
	var status, failureClass, failureCode string
	var totalAttempts int
	var failedTimestamps bool
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT status, total_attempts, failure_class, failure_code,
			       first_failed_at IS NOT NULL AND last_failed_at IS NOT NULL
			FROM embedding_jobs
			WHERE embedding_job_id = $1::uuid
		`, jobID).Scan(&status, &totalAttempts, &failureClass, &failureCode, &failedTimestamps)
	}))
	require.Equal(t, want.status, status)
	require.Equal(t, want.totalAttempts, totalAttempts)
	require.Equal(t, want.failureClass, failureClass)
	require.Equal(t, want.failureCode, failureCode)
	require.Equal(t, want.failedTimestamps, failedTimestamps)
}

func insertLegacyEmbeddingFailureFixture(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	profileID string,
	contractID string,
	fixture legacyEmbeddingFailureFixture,
) error {
	documentVersion := fixture.documentVersion
	if documentVersion == 0 {
		documentVersion = 1
	}
	jobDocumentVersion := fixture.jobDocumentVersion
	if jobDocumentVersion == 0 {
		jobDocumentVersion = documentVersion
	}
	searchState := "pending"
	if fixture.status == "failed" {
		searchState = "failed"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO search_documents (
			team_id, search_document_id, owner_profile_id, source_kind, source_id,
			source_version, document_version, embedding_contract_id,
			embedding_dimensions, search_state, document_text, document_hash,
			embedding_error
		) VALUES (
			$1::uuid, $2::uuid, $3::uuid, 'evidence', $4::uuid,
			1, $5, $6::uuid, 3, $7, $8, $9, $10
		)
	`, teamID, fixture.documentID, profileID, fixture.sourceID, documentVersion, contractID,
		searchState, "legacy embedding "+fixture.jobID,
		"sha256:"+strings.ReplaceAll(fixture.jobID, "-", ""), fixture.errorMessage); err != nil {
		return err
	}
	var completedAt any
	if fixture.status == "failed" {
		completedAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO embedding_jobs (
			team_id, embedding_job_id, search_document_id, owner_profile_id,
			source_kind, source_id, source_version, document_version,
			embedding_contract_id, embedding_dimensions, status, attempts,
			max_attempts, error, completed_at
		) VALUES (
			$1::uuid, $2::uuid, $3::uuid, $4::uuid,
			'evidence', $5::uuid, 1, $6, $7::uuid, 3,
			$8, $9, 20, $10, $11
		)
	`, teamID, fixture.jobID, fixture.documentID, profileID, fixture.sourceID,
		jobDocumentVersion, contractID, fixture.status, fixture.attempts, fixture.errorMessage, completedAt)
	return err
}
