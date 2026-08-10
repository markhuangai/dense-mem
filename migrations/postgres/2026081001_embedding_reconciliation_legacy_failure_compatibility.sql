-- +goose NO TRANSACTION
-- +goose Up
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
        -- Preserve explicit classifications from current workers.
        IF NEW.failure_class = 'permanent' AND NEW.failure_code = 'unknown_embedding_failure' THEN
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
BEGIN
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
CREATE OR REPLACE PROCEDURE dense_mem_backfill_embedding_reconciliation_2026081001()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    updated_rows INTEGER;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    LOOP
        ALTER TABLE embedding_jobs DISABLE TRIGGER embedding_jobs_failure_compatibility_after;

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
        SELECT count(changed.team_id)
        INTO updated_rows
        FROM changed
        CROSS JOIN (SELECT count(*) FROM projection_updates) AS projection_update_count;

        ALTER TABLE embedding_jobs ENABLE TRIGGER embedding_jobs_failure_compatibility_after;
        COMMIT;
        EXIT WHEN updated_rows = 0;
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
    END LOOP;

    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);

    PERFORM pg_advisory_xact_lock(hashtextextended(
        concat_ws('|', grouped.team_id::text, grouped.embedding_contract_id::text,
                  grouped.embedding_dimensions::text, grouped.source_kind,
                  grouped.failure_class, grouped.failure_code), 0
    ))
    FROM (
        SELECT DISTINCT team_id, embedding_contract_id, embedding_dimensions,
                        source_kind, failure_class, failure_code
        FROM embedding_jobs
        WHERE first_failed_at IS NOT NULL
        AND status IN ('queued', 'processing', 'failed')
    ) AS grouped;

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

CALL dense_mem_backfill_embedding_reconciliation_2026081001();
DROP PROCEDURE dense_mem_backfill_embedding_reconciliation_2026081001();

-- +goose Down
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
