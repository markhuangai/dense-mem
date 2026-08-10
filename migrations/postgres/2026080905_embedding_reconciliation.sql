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

-- Older workers only increment attempts while claiming a job. Keep the new
-- lifetime counter valid during a rolling deployment until every worker has
-- adopted the paired update.
CREATE OR REPLACE FUNCTION dense_mem_sync_embedding_job_total_attempts()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    IF NEW.total_attempts < NEW.attempts THEN
        NEW.total_attempts := NEW.attempts;
    END IF;
    RETURN NEW;
END
$function$;

DROP TRIGGER IF EXISTS embedding_jobs_total_attempts_compatibility_trigger ON embedding_jobs;
CREATE TRIGGER embedding_jobs_total_attempts_compatibility_trigger
    BEFORE INSERT OR UPDATE OF attempts, total_attempts ON embedding_jobs
    FOR EACH ROW
    EXECUTE FUNCTION dense_mem_sync_embedding_job_total_attempts();

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
CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080905()
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

CALL dense_mem_backfill_embedding_reconciliation_2026080905();
DROP PROCEDURE dense_mem_backfill_embedding_reconciliation_2026080905();

-- Install the checks after the committed backfill so old workers cannot touch
-- a row whose legacy attempts have not yet been repaired. Validation follows
-- after the concurrent indexes in this migration.
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


-- Indexes are installed in the same no-transaction migration so the scan and operator projections are ready together.

-- Concurrent indexes keep the daily scan and operator projection from taking
-- an access-exclusive lock on the live embedding tables.
DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_reconciliation_failed_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_reconciliation_failed_idx
    ON embedding_jobs(
        embedding_contract_id, embedding_dimensions,
        (COALESCE(last_failed_at, updated_at)), embedding_job_id
    )
    WHERE status = 'failed' AND failure_class <> 'permanent';

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_reconciliation_team_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_reconciliation_team_idx
    ON embedding_jobs(team_id, embedding_contract_id, embedding_dimensions,
                      status, source_kind, updated_at, embedding_job_id)
    WHERE status IN ('queued', 'processing', 'failed');

DROP INDEX CONCURRENTLY IF EXISTS embedding_failure_incidents_open_idx;
CREATE INDEX CONCURRENTLY embedding_failure_incidents_open_idx
    ON embedding_failure_incidents(
        embedding_contract_id, embedding_dimensions, status,
        failure_class, failure_code, last_seen_at DESC
    );



DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_incident_resolution_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_incident_resolution_idx
    ON embedding_jobs(
        team_id, embedding_contract_id, embedding_dimensions,
        source_kind, failure_class, failure_code, status
    )
    WHERE status IN ('queued', 'processing', 'failed');


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


-- The compatibility triggers and legacy backfill run after the schema, indexes, and constraint validation above.
-- +goose StatementBegin

-- The compatibility triggers and functions are removable through the goose
-- Down section. The backfill below is an irreversible data boundary: it
-- rewrites failure_class, failure_code, total_attempts, first_failed_at, and
-- last_failed_at on existing rows, and the previous values are not retained.
-- The Down section cannot restore those rewritten row values; accepted
-- evidence and search history remain append-only.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE OR REPLACE FUNCTION dense_mem_classify_embedding_failure_compatibility(error_value TEXT)
RETURNS TABLE (failure_class TEXT, failure_code TEXT)
LANGUAGE SQL
IMMUTABLE
AS $function$
    WITH normalized AS (
        SELECT lower(COALESCE(error_value, '')) AS error_text
    )
    SELECT CASE
               WHEN error_text LIKE '%insufficient_quota%'
                 OR error_text LIKE '%quota_exhausted%'
                 OR error_text LIKE '%exceeded your current quota%'
                   THEN 'provider_action_required'
               WHEN error_text ~ 'status([^0-9]{0,12})401'
                   THEN 'provider_action_required'
               WHEN error_text ~ 'status([^0-9]{0,12})403'
                   THEN 'provider_action_required'
               WHEN error_text ~ 'status([^0-9]{0,12})429'
                 OR error_text LIKE '%rate limit%'
                   THEN 'transient'
               WHEN error_text LIKE '%embedding request timed out%'
                 OR error_text LIKE '%context deadline exceeded%'
                 OR error_text LIKE '%client.timeout exceeded%'
                 OR error_text LIKE '%i/o timeout%'
                 OR error_text LIKE '%tls handshake timeout%'
                   THEN 'transient'
               WHEN error_text LIKE '%connection reset%'
                 OR error_text LIKE '%connection refused%'
                 OR error_text LIKE '%network is unreachable%'
                 OR error_text LIKE '%no such host%'
                 OR error_text LIKE '%temporary failure in name resolution%'
                 OR error_text LIKE '%unexpected eof%'
                   THEN 'transient'
               WHEN error_text LIKE '%provider is unavailable%'
                 OR error_text ~ 'status([^0-9]{0,12})5[0-9]{2}'
                   THEN 'transient'
               ELSE 'permanent'
           END,
           CASE
               WHEN error_text LIKE '%insufficient_quota%'
                 OR error_text LIKE '%quota_exhausted%'
                 OR error_text LIKE '%exceeded your current quota%'
                   THEN 'provider_quota_exhausted'
               WHEN error_text ~ 'status([^0-9]{0,12})401'
                   THEN 'provider_authentication_failed'
               WHEN error_text ~ 'status([^0-9]{0,12})403'
                   THEN 'provider_permission_denied'
               WHEN error_text ~ 'status([^0-9]{0,12})429'
                 OR error_text LIKE '%rate limit%'
                   THEN 'provider_rate_limited'
               WHEN error_text LIKE '%embedding request timed out%'
                 OR error_text LIKE '%context deadline exceeded%'
                 OR error_text LIKE '%client.timeout exceeded%'
                 OR error_text LIKE '%i/o timeout%'
                 OR error_text LIKE '%tls handshake timeout%'
                   THEN 'provider_timeout'
               WHEN error_text LIKE '%connection reset%'
                 OR error_text LIKE '%connection refused%'
                 OR error_text LIKE '%network is unreachable%'
                 OR error_text LIKE '%no such host%'
                 OR error_text LIKE '%temporary failure in name resolution%'
                 OR error_text LIKE '%unexpected eof%'
                   THEN 'provider_network_error'
               WHEN error_text LIKE '%provider is unavailable%'
                 OR error_text ~ 'status([^0-9]{0,12})5[0-9]{2}'
                   THEN 'provider_server_error'
               ELSE 'unknown_embedding_failure'
           END
    FROM normalized;
$function$;

CREATE OR REPLACE FUNCTION dense_mem_classify_embedding_job_failure_compatibility()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
    classification RECORD;
BEGIN
    IF NEW.status = 'queued' AND btrim(COALESCE(NEW.error, '')) = '' THEN
        RETURN NEW;
    END IF;
    IF NEW.status IN ('queued', 'failed') THEN
        -- A pre-reconciliation worker leaves the new columns at their defaults.
        -- Reclassify changed errors from old workers, while preserving explicit
        -- classifications from current workers through a transaction-local guard.
        IF TG_OP = 'UPDATE'
           AND COALESCE(current_setting('app.embedding_job_failure_writer', true), '') <> 'current'
           AND NEW.error IS DISTINCT FROM OLD.error
           AND NEW.failure_class IS NOT DISTINCT FROM OLD.failure_class
           AND NEW.failure_code IS NOT DISTINCT FROM OLD.failure_code THEN
            SELECT *
            INTO classification
            FROM dense_mem_classify_embedding_failure_compatibility(NEW.error);
            NEW.failure_class := classification.failure_class;
            NEW.failure_code := classification.failure_code;
        ELSIF NEW.failure_class = 'permanent' AND NEW.failure_code = 'unknown_embedding_failure' THEN
            SELECT *
            INTO classification
            FROM dense_mem_classify_embedding_failure_compatibility(NEW.error);
            NEW.failure_class := classification.failure_class;
            NEW.failure_code := classification.failure_code;
        END IF;
        NEW.total_attempts := GREATEST(NEW.total_attempts, NEW.attempts);
        IF TG_OP = 'INSERT' THEN
            NEW.first_failed_at := COALESCE(NEW.first_failed_at, now());
            NEW.last_failed_at := COALESCE(NEW.last_failed_at, now());
        ELSIF OLD.status IS DISTINCT FROM NEW.status
           OR OLD.failure_class IS DISTINCT FROM NEW.failure_class
           OR OLD.failure_code IS DISTINCT FROM NEW.failure_code THEN
            NEW.first_failed_at := COALESCE(NEW.first_failed_at, now());
            NEW.last_failed_at := COALESCE(NEW.last_failed_at, now());
        END IF;
    END IF;
    RETURN NEW;
END
$function$;

CREATE OR REPLACE FUNCTION dense_mem_record_embedding_job_failure_compatibility()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $function$
DECLARE
    affected_count BIGINT;
    old_failure_key TEXT;
    new_failure_key TEXT;
BEGIN
    IF COALESCE(current_setting('app.embedding_reconciliation_backfill', true), '') = 'on' THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE'
       AND (OLD.failure_class IS DISTINCT FROM NEW.failure_class
            OR OLD.failure_code IS DISTINCT FROM NEW.failure_code) THEN
        old_failure_key := concat_ws('|', OLD.team_id::text, OLD.embedding_contract_id::text,
                                     OLD.embedding_dimensions::text, OLD.source_kind,
                                     OLD.failure_class, OLD.failure_code);
        new_failure_key := concat_ws('|', NEW.team_id::text, NEW.embedding_contract_id::text,
                                     NEW.embedding_dimensions::text, NEW.source_kind,
                                     NEW.failure_class, NEW.failure_code);
        IF old_failure_key < new_failure_key THEN
            PERFORM pg_advisory_xact_lock(hashtextextended(old_failure_key, 0));
            PERFORM pg_advisory_xact_lock(hashtextextended(new_failure_key, 0));
        ELSE
            PERFORM pg_advisory_xact_lock(hashtextextended(new_failure_key, 0));
            PERFORM pg_advisory_xact_lock(hashtextextended(old_failure_key, 0));
        END IF;
        WITH remaining AS (
            SELECT incident.team_id, incident.incident_id,
                   count(job.embedding_job_id) AS affected_job_count
            FROM embedding_failure_incidents AS incident
            LEFT JOIN embedding_jobs AS job
              ON job.team_id = incident.team_id
             AND job.embedding_contract_id = incident.embedding_contract_id
             AND job.embedding_dimensions = incident.embedding_dimensions
             AND job.source_kind = incident.source_kind
             AND job.failure_class = incident.failure_class
             AND job.failure_code = incident.failure_code
             AND job.first_failed_at IS NOT NULL
             AND job.status IN ('queued', 'processing', 'failed')
            WHERE incident.team_id = OLD.team_id
              AND incident.embedding_contract_id = OLD.embedding_contract_id
              AND incident.embedding_dimensions = OLD.embedding_dimensions
              AND incident.source_kind = OLD.source_kind
              AND incident.failure_class = OLD.failure_class
              AND incident.failure_code = OLD.failure_code
              AND incident.status IN ('open', 'recovering')
            GROUP BY incident.team_id, incident.incident_id
        )
        UPDATE embedding_failure_incidents AS incident
        SET status = CASE WHEN remaining.affected_job_count = 0 THEN 'resolved' ELSE incident.status END,
            resolved_at = CASE WHEN remaining.affected_job_count = 0 THEN now() ELSE NULL END,
            affected_job_count = remaining.affected_job_count,
            updated_at = now()
        FROM remaining
        WHERE incident.team_id = remaining.team_id
          AND incident.incident_id = remaining.incident_id;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.status IN ('completed', 'stale')
       AND OLD.status IN ('queued', 'processing', 'failed') THEN
        PERFORM pg_advisory_xact_lock(hashtextextended(
            concat_ws('|', NEW.team_id::text, NEW.embedding_contract_id::text,
                      NEW.embedding_dimensions::text, NEW.source_kind,
                      NEW.failure_class, NEW.failure_code), 0
        ));
        WITH remaining AS (
            SELECT incident.team_id, incident.incident_id,
                   count(job.embedding_job_id) AS affected_job_count
            FROM embedding_failure_incidents AS incident
            LEFT JOIN embedding_jobs AS job
              ON job.team_id = incident.team_id
             AND job.embedding_contract_id = incident.embedding_contract_id
             AND job.embedding_dimensions = incident.embedding_dimensions
             AND job.source_kind = incident.source_kind
             AND job.failure_class = incident.failure_class
             AND job.failure_code = incident.failure_code
             AND job.first_failed_at IS NOT NULL
             AND job.status IN ('queued', 'processing', 'failed')
            WHERE incident.team_id = NEW.team_id
              AND incident.embedding_contract_id = NEW.embedding_contract_id
              AND incident.embedding_dimensions = NEW.embedding_dimensions
              AND incident.source_kind = NEW.source_kind
              AND incident.failure_class = NEW.failure_class
              AND incident.failure_code = NEW.failure_code
              AND incident.status IN ('open', 'recovering')
            GROUP BY incident.team_id, incident.incident_id
        )
        UPDATE embedding_failure_incidents AS incident
        SET status = CASE WHEN remaining.affected_job_count = 0 THEN 'resolved' ELSE incident.status END,
            resolved_at = CASE WHEN remaining.affected_job_count = 0 THEN now() ELSE NULL END,
            affected_job_count = remaining.affected_job_count,
            updated_at = now()
        FROM remaining
        WHERE incident.team_id = remaining.team_id
          AND incident.incident_id = remaining.incident_id;
        RETURN NEW;
    END IF;
    IF NEW.status = 'queued' AND btrim(COALESCE(NEW.error, '')) = '' THEN
        RETURN NEW;
    END IF;
    IF NEW.status NOT IN ('queued', 'failed') THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE'
       AND OLD.status = NEW.status
       AND OLD.error IS NOT DISTINCT FROM NEW.error
       AND OLD.failure_class = NEW.failure_class
       AND OLD.failure_code = NEW.failure_code
       AND OLD.first_failed_at IS NOT DISTINCT FROM NEW.first_failed_at
       AND OLD.last_failed_at IS NOT DISTINCT FROM NEW.last_failed_at THEN
        RETURN NEW;
    END IF;

    PERFORM pg_advisory_xact_lock(hashtextextended(
        concat_ws('|', NEW.team_id::text, NEW.embedding_contract_id::text,
                  NEW.embedding_dimensions::text, NEW.source_kind,
                  NEW.failure_class, NEW.failure_code), 0
    ));

    UPDATE search_documents AS document
    SET search_state = CASE WHEN NEW.status = 'failed' THEN 'failed' ELSE 'pending' END,
        embedding_error = left(COALESCE(NEW.error, ''), 1024),
        updated_at = now()
    WHERE document.team_id = NEW.team_id
      AND document.search_document_id = NEW.search_document_id
      AND document.source_version = NEW.source_version
      AND document.projection_format_version = NEW.projection_format_version
      AND document.projection_generation_id IS NOT DISTINCT FROM NEW.projection_generation_id
      AND document.document_version = NEW.document_version
      AND document.embedding_contract_id = NEW.embedding_contract_id
      AND document.embedding_dimensions = NEW.embedding_dimensions;

    SELECT count(*)
    INTO affected_count
    FROM embedding_jobs AS job
    WHERE job.team_id = NEW.team_id
      AND job.embedding_contract_id = NEW.embedding_contract_id
      AND job.embedding_dimensions = NEW.embedding_dimensions
      AND job.source_kind = NEW.source_kind
      AND job.failure_class = NEW.failure_class
      AND job.failure_code = NEW.failure_code
      AND job.first_failed_at IS NOT NULL
      AND job.status IN ('queued', 'processing', 'failed');

    UPDATE embedding_failure_incidents
    SET affected_job_count = affected_count,
        last_seen_at = now(),
        resolved_at = NULL,
        recovering_at = NULL,
        updated_at = now()
    WHERE team_id = NEW.team_id
      AND embedding_contract_id = NEW.embedding_contract_id
      AND embedding_dimensions = NEW.embedding_dimensions
      AND source_kind = NEW.source_kind
      AND failure_class = NEW.failure_class
      AND failure_code = NEW.failure_code
      AND status = 'open';
    IF FOUND THEN
        RETURN NEW;
    END IF;

    UPDATE embedding_failure_incidents
    SET status = 'open',
        affected_job_count = affected_count,
        last_seen_at = now(),
        resolved_at = NULL,
        recovering_at = NULL,
        updated_at = now()
    WHERE team_id = NEW.team_id
      AND embedding_contract_id = NEW.embedding_contract_id
      AND embedding_dimensions = NEW.embedding_dimensions
      AND source_kind = NEW.source_kind
      AND failure_class = NEW.failure_class
      AND failure_code = NEW.failure_code
      AND status = 'recovering';
    IF FOUND THEN
        RETURN NEW;
    END IF;

    UPDATE embedding_failure_incidents
    SET status = 'open',
        affected_job_count = affected_count,
        last_seen_at = now(),
        resolved_at = NULL,
        recovering_at = NULL,
        updated_at = now()
    WHERE team_id = NEW.team_id
      AND embedding_contract_id = NEW.embedding_contract_id
      AND embedding_dimensions = NEW.embedding_dimensions
      AND source_kind = NEW.source_kind
      AND failure_class = NEW.failure_class
      AND failure_code = NEW.failure_code
      AND status = 'resolved';
    IF FOUND THEN
        RETURN NEW;
    END IF;

    INSERT INTO embedding_failure_incidents (
        team_id, embedding_contract_id, embedding_dimensions, source_kind,
        failure_class, failure_code, status, affected_job_count,
        first_seen_at, last_seen_at, updated_at
    ) VALUES (
        NEW.team_id, NEW.embedding_contract_id, NEW.embedding_dimensions, NEW.source_kind,
        NEW.failure_class, NEW.failure_code, 'open', affected_count,
        now(), now(), now()
    )
    ON CONFLICT (team_id, embedding_contract_id, embedding_dimensions, source_kind, failure_class, failure_code, status)
    DO UPDATE SET affected_job_count = EXCLUDED.affected_job_count,
                  last_seen_at = now(), updated_at = now();
    RETURN NEW;
END
$function$;

DROP TRIGGER IF EXISTS embedding_jobs_failure_compatibility_before ON embedding_jobs;
CREATE TRIGGER embedding_jobs_failure_compatibility_before
    BEFORE INSERT OR UPDATE OF status, error, failure_class, failure_code, attempts, total_attempts
    ON embedding_jobs
    FOR EACH ROW
    EXECUTE FUNCTION dense_mem_classify_embedding_job_failure_compatibility();

DROP TRIGGER IF EXISTS embedding_jobs_failure_compatibility_after ON embedding_jobs;
CREATE TRIGGER embedding_jobs_failure_compatibility_after
    AFTER INSERT OR UPDATE OF status, error, failure_class, failure_code, attempts, total_attempts
    ON embedding_jobs
    FOR EACH ROW
    EXECUTE FUNCTION dense_mem_record_embedding_job_failure_compatibility();

-- +goose StatementEnd

-- Legacy workers can leave a retryable queued row with its provider error and
-- the pre-reconciliation permanent/unknown defaults. Reclassify only those
-- rows (and failed rows still missing failure timestamps); healthy queued work
-- with an empty error is deliberately excluded. Each batch commits so a large
-- upgrade can resume after interruption without retaining one transaction-wide
-- lock set.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_compatibility()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    candidate_rows INTEGER;
    updated_rows INTEGER;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    LOOP
        PERFORM set_config('app.embedding_reconciliation_backfill', 'on', true);

        WITH batch AS MATERIALIZED (
            SELECT team_id, embedding_job_id, status, error, attempts,
                   completed_at, updated_at
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
            FOR UPDATE SKIP LOCKED
        ), changed AS (
            UPDATE embedding_jobs AS job
            SET (failure_class, failure_code) = (
                    SELECT classification.failure_class, classification.failure_code
                    FROM dense_mem_classify_embedding_failure_compatibility(batch.error) AS classification
                ),
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
        SELECT (SELECT count(*) FROM batch), count(changed.team_id)
        INTO candidate_rows, updated_rows
        FROM changed
        CROSS JOIN (SELECT count(*) FROM projection_updates) AS projection_update_count;

        COMMIT;
        EXIT WHEN candidate_rows = 0;
        IF updated_rows = 0 THEN
            PERFORM pg_sleep(1);
        END IF;
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
    END LOOP;

    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    UPDATE embedding_failure_incidents AS incident
    SET status = 'open',
        affected_job_count = active.affected_job_count,
        last_seen_at = active.last_seen_at,
        resolved_at = NULL,
        recovering_at = NULL,
        updated_at = now()
    FROM (
        SELECT job.team_id, job.embedding_contract_id, job.embedding_dimensions,
               job.source_kind, job.failure_class, job.failure_code,
               count(*) AS affected_job_count, max(job.last_failed_at) AS last_seen_at
        FROM embedding_jobs AS job
        WHERE job.first_failed_at IS NOT NULL
          AND job.status IN ('queued', 'processing', 'failed')
        GROUP BY job.team_id, job.embedding_contract_id, job.embedding_dimensions,
                 job.source_kind, job.failure_class, job.failure_code
    ) AS active
    WHERE incident.team_id = active.team_id
      AND incident.embedding_contract_id = active.embedding_contract_id
      AND incident.embedding_dimensions = active.embedding_dimensions
      AND incident.source_kind = active.source_kind
      AND incident.failure_class = active.failure_class
      AND incident.failure_code = active.failure_code
      AND incident.status IN ('recovering', 'resolved');

    INSERT INTO embedding_failure_incidents (
        team_id, embedding_contract_id, embedding_dimensions, source_kind,
        failure_class, failure_code, status, affected_job_count,
        first_seen_at, last_seen_at, updated_at
    )
    SELECT job.team_id, job.embedding_contract_id, job.embedding_dimensions,
           job.source_kind, job.failure_class, job.failure_code, 'open', count(*),
           min(job.first_failed_at), max(job.last_failed_at), now()
    FROM embedding_jobs AS job
    WHERE job.first_failed_at IS NOT NULL
      AND job.status IN ('queued', 'processing', 'failed')
    GROUP BY job.team_id, job.embedding_contract_id, job.embedding_dimensions,
             job.source_kind, job.failure_class, job.failure_code
    ON CONFLICT (team_id, embedding_contract_id, embedding_dimensions, source_kind, failure_class, failure_code, status)
    DO UPDATE SET status = 'open',
                  affected_job_count = EXCLUDED.affected_job_count,
                  last_seen_at = EXCLUDED.last_seen_at,
                  resolved_at = NULL,
                  recovering_at = NULL,
                  updated_at = now();

    UPDATE embedding_failure_incidents AS incident
    SET status = 'resolved',
        affected_job_count = 0,
        resolved_at = COALESCE(incident.resolved_at, now()),
        updated_at = now()
    WHERE incident.status IN ('open', 'recovering')
      AND NOT EXISTS (
          SELECT 1
          FROM embedding_jobs AS job
          WHERE job.team_id = incident.team_id
            AND job.embedding_contract_id = incident.embedding_contract_id
            AND job.embedding_dimensions = incident.embedding_dimensions
            AND job.source_kind = incident.source_kind
            AND job.failure_class = incident.failure_class
            AND job.failure_code = incident.failure_code
            AND job.first_failed_at IS NOT NULL
            AND job.status IN ('queued', 'processing', 'failed')
      );
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_embedding_reconciliation_compatibility();
DROP PROCEDURE dense_mem_backfill_embedding_reconciliation_compatibility();


-- +goose Down
-- +goose StatementBegin
-- Validation and the legacy rewrite are irreversible. This preserves the prior
-- multi-migration boundary: compatibility objects can be removed, but the
-- validated constraints and rewritten row values cannot be safely rolled back.
DO $dense_mem_irreversible_embedding_reconciliation$
BEGIN
    RAISE EXCEPTION 'embedding reconciliation migration is irreversible because validated constraints and rewritten failure metadata cannot be restored';
END
$dense_mem_irreversible_embedding_reconciliation$;
-- +goose StatementEnd

-- These objects are intentionally unreachable because the irreversible block
-- above always aborts goose down before cleanup can run.
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
DROP TRIGGER IF EXISTS embedding_jobs_failure_compatibility_after ON embedding_jobs;
DROP TRIGGER IF EXISTS embedding_jobs_failure_compatibility_before ON embedding_jobs;
DROP FUNCTION IF EXISTS dense_mem_record_embedding_job_failure_compatibility();
DROP FUNCTION IF EXISTS dense_mem_classify_embedding_job_failure_compatibility();
DROP FUNCTION IF EXISTS dense_mem_classify_embedding_failure_compatibility(TEXT);
-- +goose StatementEnd
