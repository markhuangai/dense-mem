-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- This migration requires a coordinated application restart. New workers are
-- the only writers after it completes, so no mixed-version triggers are kept.
ALTER TABLE embedding_jobs
    ADD COLUMN IF NOT EXISTS total_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS recovery_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failure_class TEXT NOT NULL DEFAULT 'permanent',
    ADD COLUMN IF NOT EXISTS failure_code TEXT NOT NULL DEFAULT 'unknown_embedding_failure',
    ADD COLUMN IF NOT EXISTS first_failed_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS last_failed_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS last_recovered_at TIMESTAMPTZ NULL;

CREATE TABLE IF NOT EXISTS embedding_reconciliation_runs (
    reconciliation_run_id UUID NOT NULL DEFAULT gen_random_uuid(),
    embedding_contract_id UUID NOT NULL,
    embedding_dimensions INTEGER NOT NULL,
    local_run_date DATE NOT NULL,
    status TEXT NOT NULL DEFAULT 'reserved',
    candidate_cutoff TIMESTAMPTZ NOT NULL DEFAULT now(),
    worker_id TEXT NOT NULL DEFAULT '',
    lease_token UUID NULL,
    lease_until TIMESTAMPTZ NULL,
    canary_job_id UUID NULL,
    canary_attempted_at TIMESTAMPTZ NULL,
    canary_outcome TEXT NOT NULL DEFAULT '',
    canary_failure_class TEXT NOT NULL DEFAULT '',
    canary_failure_code TEXT NOT NULL DEFAULT '',
    requeued_count BIGINT NOT NULL DEFAULT 0,
    recovered_count BIGINT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (reconciliation_run_id),
    UNIQUE (embedding_contract_id, embedding_dimensions, local_run_date),
    FOREIGN KEY (embedding_contract_id, embedding_dimensions)
        REFERENCES embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT,
    CONSTRAINT embedding_reconciliation_runs_status_check
        CHECK (status IN ('reserved', 'running', 'completed', 'deferred', 'failed', 'ambiguous')),
    CONSTRAINT embedding_reconciliation_runs_outcome_check
        CHECK (canary_outcome IN ('', 'succeeded', 'failed', 'ambiguous')),
    CONSTRAINT embedding_reconciliation_runs_count_check
        CHECK (requeued_count >= 0 AND recovered_count >= 0)
);

ALTER TABLE embedding_reconciliation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding_reconciliation_runs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS embedding_reconciliation_runs_system_access ON embedding_reconciliation_runs;
CREATE POLICY embedding_reconciliation_runs_system_access ON embedding_reconciliation_runs
    FOR ALL USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

-- +goose StatementEnd

-- Each bounded batch commits independently so upgrade repair does not hold one
-- transaction-wide snapshot or lock set across the embedding job table.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080905()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    updated_rows INTEGER;
    projection_rows INTEGER;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    LOOP
        WITH batch AS MATERIALIZED (
            SELECT team_id, embedding_job_id
            FROM embedding_jobs
            WHERE total_attempts < attempts
            ORDER BY team_id, embedding_job_id
            LIMIT 1000
            FOR UPDATE
        )
        UPDATE embedding_jobs AS job
        SET total_attempts = GREATEST(job.total_attempts, job.attempts)
        FROM batch
        WHERE job.team_id = batch.team_id
          AND job.embedding_job_id = batch.embedding_job_id;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN updated_rows = 0;
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
    END LOOP;

    LOOP
        WITH batch AS MATERIALIZED (
            SELECT team_id, embedding_job_id, status, error, completed_at, updated_at
            FROM embedding_jobs
            WHERE failure_class = 'permanent'
              AND failure_code = 'unknown_embedding_failure'
              AND (first_failed_at IS NULL OR last_failed_at IS NULL)
              AND (
                  status = 'failed'
                  OR (status = 'queued' AND btrim(COALESCE(error, '')) <> '')
              )
            ORDER BY team_id, embedding_job_id
            LIMIT 1000
            FOR UPDATE
        ), changed AS (
            UPDATE embedding_jobs AS job
            SET failure_class = CASE
                    WHEN lower(batch.error) LIKE '%insufficient_quota%'
                      OR lower(batch.error) LIKE '%quota_exhausted%'
                      OR lower(batch.error) LIKE '%exceeded your current quota%'
                        THEN 'provider_action_required'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})(401|403)'
                        THEN 'provider_action_required'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})429'
                      OR lower(batch.error) LIKE '%rate limit%'
                      OR lower(batch.error) ~ 'status([^0-9]{0,12})408'
                      OR lower(batch.error) LIKE '%embedding request timed out%'
                      OR lower(batch.error) LIKE '%context deadline exceeded%'
                      OR lower(batch.error) LIKE '%client.timeout exceeded%'
                      OR lower(batch.error) LIKE '%i/o timeout%'
                      OR lower(batch.error) LIKE '%tls handshake timeout%'
                      OR lower(batch.error) LIKE '%connection reset%'
                      OR lower(batch.error) LIKE '%connection refused%'
                      OR lower(batch.error) LIKE '%network is unreachable%'
                      OR lower(batch.error) LIKE '%no such host%'
                      OR lower(batch.error) LIKE '%temporary failure in name resolution%'
                      OR lower(batch.error) LIKE '%unexpected eof%'
                      OR lower(batch.error) LIKE '%provider is unavailable%'
                      OR lower(batch.error) ~ 'status([^0-9]{0,12})5[0-9]{2}'
                        THEN 'transient'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})413'
                        THEN 'permanent'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})4[0-9]{2}'
                        THEN 'provider_action_required'
                    ELSE 'permanent'
                END,
                failure_code = CASE
                    WHEN lower(batch.error) LIKE '%insufficient_quota%'
                      OR lower(batch.error) LIKE '%quota_exhausted%'
                      OR lower(batch.error) LIKE '%exceeded your current quota%'
                        THEN 'provider_quota_exhausted'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})401'
                        THEN 'provider_authentication_failed'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})403'
                        THEN 'provider_permission_denied'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})429'
                      OR lower(batch.error) LIKE '%rate limit%'
                        THEN 'provider_rate_limited'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})408'
                        THEN 'provider_timeout'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})413'
                        THEN 'embedding_input_rejected'
                    WHEN lower(batch.error) ~ 'status([^0-9]{0,12})4[0-9]{2}'
                        THEN 'provider_contract_rejected'
                    WHEN lower(batch.error) LIKE '%embedding request timed out%'
                      OR lower(batch.error) LIKE '%context deadline exceeded%'
                      OR lower(batch.error) LIKE '%client.timeout exceeded%'
                      OR lower(batch.error) LIKE '%i/o timeout%'
                      OR lower(batch.error) LIKE '%tls handshake timeout%'
                        THEN 'provider_timeout'
                    WHEN lower(batch.error) LIKE '%connection reset%'
                      OR lower(batch.error) LIKE '%connection refused%'
                      OR lower(batch.error) LIKE '%network is unreachable%'
                      OR lower(batch.error) LIKE '%no such host%'
                      OR lower(batch.error) LIKE '%temporary failure in name resolution%'
                      OR lower(batch.error) LIKE '%unexpected eof%'
                        THEN 'provider_network_error'
                    WHEN lower(batch.error) LIKE '%provider is unavailable%'
                      OR lower(batch.error) ~ 'status([^0-9]{0,12})5[0-9]{2}'
                        THEN 'provider_server_error'
                    ELSE 'unknown_embedding_failure'
                END,
                total_attempts = GREATEST(job.total_attempts, job.attempts),
                first_failed_at = COALESCE(job.first_failed_at, batch.completed_at, batch.updated_at, now()),
                last_failed_at = COALESCE(job.last_failed_at, batch.completed_at, batch.updated_at, now()),
                updated_at = now()
            FROM batch
            WHERE job.team_id = batch.team_id
              AND job.embedding_job_id = batch.embedding_job_id
            RETURNING job.team_id, job.search_document_id, job.status, job.error,
                      job.source_version, job.projection_format_version,
                      job.projection_generation_id, job.document_version,
                      job.embedding_contract_id, job.embedding_dimensions
        ), projection_updates AS (
            UPDATE search_documents AS document
            SET search_state = CASE WHEN changed.status = 'failed' THEN 'failed' ELSE 'pending' END,
                embedding_error = left(COALESCE(changed.error, ''), 1024),
                updated_at = now()
            FROM changed
            WHERE document.team_id = changed.team_id
              AND document.search_document_id = changed.search_document_id
              AND document.source_version = changed.source_version
              AND document.projection_format_version = changed.projection_format_version
              AND document.projection_generation_id IS NOT DISTINCT FROM changed.projection_generation_id
              AND document.document_version = changed.document_version
              AND document.embedding_contract_id = changed.embedding_contract_id
              AND document.embedding_dimensions = changed.embedding_dimensions
            RETURNING 1
        )
        SELECT (SELECT count(*) FROM changed), (SELECT count(*) FROM projection_updates)
        INTO updated_rows, projection_rows;
        COMMIT;
        EXIT WHEN updated_rows = 0;
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
    END LOOP;

    LOOP
        WITH superseded_batch AS MATERIALIZED (
            SELECT job.team_id, job.embedding_job_id
            FROM embedding_jobs AS job
            WHERE job.status = 'failed'
              AND NOT EXISTS (
                  SELECT 1
                  FROM search_documents AS document
                  WHERE document.team_id = job.team_id
                    AND document.search_document_id = job.search_document_id
                    AND document.source_kind = job.source_kind
                    AND document.source_id = job.source_id
                    AND document.source_version = job.source_version
                    AND document.projection_format_version = job.projection_format_version
                    AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
                    AND document.document_version = job.document_version
                    AND document.embedding_contract_id = job.embedding_contract_id
                    AND document.embedding_dimensions = job.embedding_dimensions
              )
            ORDER BY job.team_id, job.embedding_job_id
            LIMIT 1000
            FOR UPDATE OF job
        )
        UPDATE embedding_jobs AS job
        SET status = 'stale',
            error = 'superseded by newer document version',
            completed_at = COALESCE(job.completed_at, job.updated_at, now()),
            lease_until = NULL,
            worker_id = '',
            updated_at = now()
        FROM superseded_batch AS batch
        WHERE job.team_id = batch.team_id
          AND job.embedding_job_id = batch.embedding_job_id;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN updated_rows = 0;
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
    END LOOP;
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_embedding_reconciliation_2026080905();
DROP PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080905();

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE embedding_jobs
    DROP CONSTRAINT IF EXISTS embedding_jobs_total_attempts_check,
    DROP CONSTRAINT IF EXISTS embedding_jobs_recovery_count_check,
    DROP CONSTRAINT IF EXISTS embedding_jobs_failure_class_check,
    DROP CONSTRAINT IF EXISTS embedding_jobs_failure_code_check;

ALTER TABLE embedding_jobs
    ADD CONSTRAINT embedding_jobs_total_attempts_check
        CHECK (total_attempts >= attempts AND total_attempts >= 0) NOT VALID,
    ADD CONSTRAINT embedding_jobs_recovery_count_check
        CHECK (recovery_count >= 0) NOT VALID,
    ADD CONSTRAINT embedding_jobs_failure_class_check
        CHECK (failure_class IN ('transient', 'provider_action_required', 'permanent')) NOT VALID,
    ADD CONSTRAINT embedding_jobs_failure_code_check
        CHECK (failure_code IN (
            'provider_rate_limited', 'provider_timeout',
            'provider_network_error', 'provider_server_error',
            'provider_quota_exhausted', 'provider_authentication_failed',
            'provider_permission_denied', 'provider_contract_rejected',
            'provider_response_invalid', 'embedding_input_rejected',
            'embedding_contract_mismatch', 'unknown_embedding_failure'
        )) NOT VALID;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_reconciliation_failed_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_reconciliation_failed_idx
    ON embedding_jobs(
        embedding_contract_id, embedding_dimensions,
        (COALESCE(last_failed_at, updated_at)), embedding_job_id
    )
    WHERE status = 'failed' AND failure_class <> 'permanent';

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_failure_groups_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_failure_groups_idx
    ON embedding_jobs(
        embedding_contract_id, embedding_dimensions,
        team_id, source_kind, failure_class, failure_code, status
    )
    INCLUDE (first_failed_at, last_failed_at)
    WHERE first_failed_at IS NOT NULL
      AND status IN ('queued', 'processing', 'failed');

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_total_attempts_check;
ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_recovery_count_check;
ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_failure_class_check;
ALTER TABLE embedding_jobs
    VALIDATE CONSTRAINT embedding_jobs_failure_code_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_irreversible_embedding_reconciliation$
BEGIN
    RAISE EXCEPTION 'embedding reconciliation migration is irreversible because validated constraints and rewritten failure metadata cannot be restored';
END
$dense_mem_irreversible_embedding_reconciliation$;
-- +goose StatementEnd
