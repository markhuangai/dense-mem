-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- These fields preserve lifetime history while allowing a successful daily
-- recovery cycle to reset the short inline-attempt budget.
ALTER TABLE embedding_jobs
    ADD COLUMN IF NOT EXISTS total_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS recovery_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failure_class TEXT NOT NULL DEFAULT 'permanent',
    ADD COLUMN IF NOT EXISTS failure_code TEXT NOT NULL DEFAULT 'unknown_embedding_failure',
    ADD COLUMN IF NOT EXISTS first_failed_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS last_failed_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS last_recovered_at TIMESTAMPTZ NULL;

CREATE TABLE IF NOT EXISTS embedding_failure_incidents (
    team_id UUID NOT NULL,
    incident_id UUID NOT NULL DEFAULT gen_random_uuid(),
    embedding_contract_id UUID NOT NULL,
    embedding_dimensions INTEGER NOT NULL,
    source_kind TEXT NOT NULL,
    failure_class TEXT NOT NULL,
    failure_code TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    affected_job_count BIGINT NOT NULL DEFAULT 0,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    recovering_at TIMESTAMPTZ NULL,
    resolved_at TIMESTAMPTZ NULL,
    last_reconciliation_run_id UUID NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, incident_id),
    UNIQUE (team_id, embedding_contract_id, embedding_dimensions, source_kind, failure_class, failure_code, status),
    FOREIGN KEY (embedding_contract_id, embedding_dimensions)
        REFERENCES embedding_contracts(embedding_contract_id, dimensions) ON DELETE RESTRICT,
    CONSTRAINT embedding_failure_incidents_source_kind_check
        CHECK (source_kind IN ('evidence', 'relationship', 'entity')),
    CONSTRAINT embedding_failure_incidents_class_check
        CHECK (failure_class IN ('transient', 'provider_action_required', 'permanent')),
    CONSTRAINT embedding_failure_incidents_code_check
        CHECK (failure_code IN (
            'provider_rate_limited', 'provider_timeout',
            'provider_network_error', 'provider_server_error',
            'provider_quota_exhausted', 'provider_authentication_failed',
            'provider_permission_denied', 'provider_contract_rejected',
            'provider_response_invalid', 'embedding_input_rejected',
            'embedding_contract_mismatch', 'unknown_embedding_failure'
        )),
    CONSTRAINT embedding_failure_incidents_status_check
        CHECK (status IN ('open', 'recovering', 'resolved')),
    CONSTRAINT embedding_failure_incidents_count_check CHECK (affected_job_count >= 0),
    CONSTRAINT embedding_failure_incidents_resolved_time_check
        CHECK ((status = 'resolved' AND resolved_at IS NOT NULL) OR status <> 'resolved')
);

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

ALTER TABLE embedding_failure_incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding_failure_incidents FORCE ROW LEVEL SECURITY;
ALTER TABLE embedding_reconciliation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding_reconciliation_runs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS embedding_failure_incidents_select ON embedding_failure_incidents;
CREATE POLICY embedding_failure_incidents_select ON embedding_failure_incidents
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid)
    );
DROP POLICY IF EXISTS embedding_failure_incidents_write ON embedding_failure_incidents;
CREATE POLICY embedding_failure_incidents_write ON embedding_failure_incidents
    FOR ALL USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (current_setting('app.tx_mode', true) = 'team'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid)
    ) WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (current_setting('app.tx_mode', true) = 'team'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid)
    );
DROP POLICY IF EXISTS embedding_reconciliation_runs_system_access ON embedding_reconciliation_runs;
CREATE POLICY embedding_reconciliation_runs_system_access ON embedding_reconciliation_runs
    FOR ALL USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

-- +goose StatementEnd

-- The procedure commits every bounded batch so upgrade repair does not retain
-- row locks or one transaction-wide snapshot across the legacy job table.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080904()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    updated_rows INTEGER;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    LOOP
        WITH batch AS (
            SELECT team_id, embedding_job_id
            FROM embedding_jobs
            WHERE total_attempts < attempts
            ORDER BY team_id, embedding_job_id
            LIMIT 1000
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

    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    LOOP
        WITH batch AS (
            SELECT team_id, embedding_job_id, error, completed_at, updated_at
            FROM embedding_jobs
            WHERE status = 'failed'
              AND (first_failed_at IS NULL OR last_failed_at IS NULL)
            ORDER BY team_id, embedding_job_id
            LIMIT 1000
        )
        UPDATE embedding_jobs AS job
        SET failure_class = CASE
                WHEN lower(batch.error) LIKE '%insufficient_quota%'
                  OR lower(batch.error) LIKE '%quota_exhausted%'
                  OR lower(batch.error) LIKE '%exceeded your current quota%'
                    THEN 'provider_action_required'
                WHEN lower(batch.error) ~ 'status([^0-9]{0,12})401'
                  OR lower(batch.error) ~ 'status([^0-9]{0,12})403'
                    THEN 'provider_action_required'
                WHEN lower(batch.error) ~ 'status([^0-9]{0,12})429'
                  OR lower(batch.error) LIKE '%rate limit%'
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
            first_failed_at = COALESCE(job.first_failed_at, batch.completed_at, batch.updated_at),
            last_failed_at = COALESCE(job.last_failed_at, batch.completed_at, batch.updated_at)
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
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_embedding_reconciliation_2026080904();
DROP PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080904();

-- Install the checks after the committed backfill so old workers cannot touch
-- a row whose legacy attempts have not yet been repaired. Validation is 0907.
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

-- Existing failed rows become visible without being requeued. The scheduler
-- owns the first recovery attempt after this migration.
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

INSERT INTO embedding_failure_incidents (
    team_id, embedding_contract_id, embedding_dimensions, source_kind,
    failure_class, failure_code, status, affected_job_count,
    first_seen_at, last_seen_at, updated_at
)
SELECT job.team_id, job.embedding_contract_id, job.embedding_dimensions,
       job.source_kind, job.failure_class, job.failure_code, 'open', count(*),
       COALESCE(min(job.first_failed_at), min(job.updated_at)),
       COALESCE(max(job.last_failed_at), max(job.updated_at)), now()
FROM embedding_jobs AS job
WHERE job.status = 'failed'
GROUP BY job.team_id, job.embedding_contract_id, job.embedding_dimensions,
         job.source_kind, job.failure_class, job.failure_code
ON CONFLICT (team_id, embedding_contract_id, embedding_dimensions, source_kind, failure_class, failure_code, status)
DO UPDATE SET affected_job_count = EXCLUDED.affected_job_count,
              last_seen_at = EXCLUDED.last_seen_at,
              updated_at = now();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP TABLE IF EXISTS embedding_reconciliation_runs;
DROP TABLE IF EXISTS embedding_failure_incidents;
ALTER TABLE embedding_jobs
    DROP CONSTRAINT IF EXISTS embedding_jobs_total_attempts_check,
    DROP CONSTRAINT IF EXISTS embedding_jobs_recovery_count_check,
    DROP CONSTRAINT IF EXISTS embedding_jobs_failure_class_check,
    DROP CONSTRAINT IF EXISTS embedding_jobs_failure_code_check,
    DROP COLUMN IF EXISTS total_attempts,
    DROP COLUMN IF EXISTS recovery_count,
    DROP COLUMN IF EXISTS failure_class,
    DROP COLUMN IF EXISTS failure_code,
    DROP COLUMN IF EXISTS first_failed_at,
    DROP COLUMN IF EXISTS last_failed_at,
    DROP COLUMN IF EXISTS last_recovered_at;

-- +goose StatementEnd
