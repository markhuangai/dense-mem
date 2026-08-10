-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin

-- This compatibility layer is reversible through the goose Down section. It
-- only repairs embedding-job projection state and incident metadata; accepted
-- evidence and search history remain append-only.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE OR REPLACE FUNCTION dense_mem_classify_embedding_job_failure_compatibility()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
DECLARE
    error_text TEXT := lower(COALESCE(NEW.error, ''));
BEGIN
    IF NEW.status = 'queued' AND btrim(COALESCE(NEW.error, '')) = '' THEN
        RETURN NEW;
    END IF;
    IF NEW.status IN ('queued', 'failed') THEN
        -- A pre-reconciliation worker leaves the new columns at their defaults.
        -- Preserve explicit classifications from current workers.
        IF NEW.failure_class = 'permanent' AND NEW.failure_code = 'unknown_embedding_failure' THEN
            IF error_text LIKE '%insufficient_quota%'
              OR error_text LIKE '%quota_exhausted%'
              OR error_text LIKE '%exceeded your current quota%' THEN
                NEW.failure_class := 'provider_action_required';
                NEW.failure_code := 'provider_quota_exhausted';
            ELSIF error_text ~ 'status([^0-9]{0,12})401' THEN
                NEW.failure_class := 'provider_action_required';
                NEW.failure_code := 'provider_authentication_failed';
            ELSIF error_text ~ 'status([^0-9]{0,12})403' THEN
                NEW.failure_class := 'provider_action_required';
                NEW.failure_code := 'provider_permission_denied';
            ELSIF error_text ~ 'status([^0-9]{0,12})429'
              OR error_text LIKE '%rate limit%' THEN
                NEW.failure_class := 'transient';
                NEW.failure_code := 'provider_rate_limited';
            ELSIF error_text LIKE '%embedding request timed out%'
              OR error_text LIKE '%context deadline exceeded%'
              OR error_text LIKE '%client.timeout exceeded%'
              OR error_text LIKE '%i/o timeout%'
              OR error_text LIKE '%tls handshake timeout%' THEN
                NEW.failure_class := 'transient';
                NEW.failure_code := 'provider_timeout';
            ELSIF error_text LIKE '%connection reset%'
              OR error_text LIKE '%connection refused%'
              OR error_text LIKE '%network is unreachable%'
              OR error_text LIKE '%no such host%'
              OR error_text LIKE '%temporary failure in name resolution%'
              OR error_text LIKE '%unexpected eof%' THEN
                NEW.failure_class := 'transient';
                NEW.failure_code := 'provider_network_error';
            ELSIF error_text LIKE '%provider is unavailable%'
              OR error_text ~ 'status([^0-9]{0,12})5[0-9]{2}' THEN
                NEW.failure_class := 'transient';
                NEW.failure_code := 'provider_server_error';
            END IF;
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
        )
        UPDATE embedding_jobs AS job
        SET failure_class = CASE
                WHEN lower(COALESCE(batch.error, '')) LIKE '%insufficient_quota%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%quota_exhausted%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%exceeded your current quota%'
                    THEN 'provider_action_required'
                WHEN lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})401'
                    THEN 'provider_action_required'
                WHEN lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})403'
                    THEN 'provider_action_required'
                WHEN lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})429'
                  OR lower(COALESCE(batch.error, '')) LIKE '%rate limit%'
                    THEN 'transient'
                WHEN lower(COALESCE(batch.error, '')) LIKE '%embedding request timed out%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%context deadline exceeded%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%client.timeout exceeded%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%i/o timeout%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%tls handshake timeout%'
                    THEN 'transient'
                WHEN lower(COALESCE(batch.error, '')) LIKE '%connection reset%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%connection refused%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%network is unreachable%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%no such host%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%temporary failure in name resolution%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%unexpected eof%'
                    THEN 'transient'
                WHEN lower(COALESCE(batch.error, '')) LIKE '%provider is unavailable%'
                  OR lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})5[0-9]{2}'
                    THEN 'transient'
                ELSE 'permanent'
            END,
            failure_code = CASE
                WHEN lower(COALESCE(batch.error, '')) LIKE '%insufficient_quota%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%quota_exhausted%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%exceeded your current quota%'
                    THEN 'provider_quota_exhausted'
                WHEN lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})401'
                    THEN 'provider_authentication_failed'
                WHEN lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})403'
                    THEN 'provider_permission_denied'
                WHEN lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})429'
                  OR lower(COALESCE(batch.error, '')) LIKE '%rate limit%'
                    THEN 'provider_rate_limited'
                WHEN lower(COALESCE(batch.error, '')) LIKE '%embedding request timed out%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%context deadline exceeded%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%client.timeout exceeded%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%i/o timeout%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%tls handshake timeout%'
                    THEN 'provider_timeout'
                WHEN lower(COALESCE(batch.error, '')) LIKE '%connection reset%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%connection refused%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%network is unreachable%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%no such host%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%temporary failure in name resolution%'
                  OR lower(COALESCE(batch.error, '')) LIKE '%unexpected eof%'
                    THEN 'provider_network_error'
                WHEN lower(COALESCE(batch.error, '')) LIKE '%provider is unavailable%'
                  OR lower(COALESCE(batch.error, '')) ~ 'status([^0-9]{0,12})5[0-9]{2}'
                    THEN 'provider_server_error'
                ELSE 'unknown_embedding_failure'
            END,
            total_attempts = GREATEST(job.total_attempts, job.attempts),
            first_failed_at = COALESCE(job.first_failed_at, batch.completed_at, batch.updated_at, now()),
            last_failed_at = COALESCE(job.last_failed_at, batch.completed_at, batch.updated_at, now()),
            updated_at = now()
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

-- +goose StatementEnd
