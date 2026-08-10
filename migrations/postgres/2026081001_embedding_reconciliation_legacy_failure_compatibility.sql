-- +goose Up
-- +goose StatementBegin

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
    IF TG_OP = 'UPDATE' AND NEW.status = 'stale'
       AND OLD.status IN ('queued', 'processing', 'failed') THEN
        UPDATE embedding_failure_incidents AS incident
        SET status = 'resolved',
            resolved_at = now(),
            affected_job_count = 0,
            updated_at = now()
        WHERE incident.team_id = NEW.team_id
          AND incident.embedding_contract_id = NEW.embedding_contract_id
          AND incident.embedding_dimensions = NEW.embedding_dimensions
          AND incident.source_kind = NEW.source_kind
          AND incident.failure_class = NEW.failure_class
          AND incident.failure_code = NEW.failure_code
          AND incident.status IN ('open', 'recovering')
          AND NOT EXISTS (
              SELECT 1
              FROM embedding_jobs AS remaining
              WHERE remaining.team_id = incident.team_id
                AND remaining.embedding_contract_id = incident.embedding_contract_id
                AND remaining.embedding_dimensions = incident.embedding_dimensions
                AND remaining.source_kind = incident.source_kind
                AND remaining.failure_class = incident.failure_class
                AND remaining.failure_code = incident.failure_code
                AND remaining.status IN ('queued', 'processing', 'failed')
          );
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
       AND OLD.failure_code = NEW.failure_code THEN
        RETURN NEW;
    END IF;

    UPDATE search_documents AS document
    SET search_state = CASE WHEN NEW.status = 'failed' THEN 'failed' ELSE 'pending' END,
        embedding_error = NEW.error,
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
