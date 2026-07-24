-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Lock/rewrite: changing the default takes a brief ACCESS EXCLUSIVE lock but does not rewrite the table.
-- WAL/disk: row updates are limited to legacy five-attempt work and are proportional to matching jobs.
-- RLS/roles: recovery runs only in migration mode and keeps team, owner, document, and contract identity unchanged.
-- Rollback: retry recovery is intentionally not reversed; Down restores only the legacy column default.
ALTER TABLE embedding_jobs
    ALTER COLUMN max_attempts SET DEFAULT 20;

UPDATE embedding_jobs
SET max_attempts = 20,
    updated_at = now()
WHERE max_attempts = 5
  AND status IN ('queued', 'processing');

WITH active_contract AS (
    SELECT contract.embedding_contract_id,
           contract.dimensions
    FROM search_index_generations AS generation
    JOIN embedding_contracts AS contract
      ON contract.embedding_contract_id = generation.embedding_contract_id
     AND contract.dimensions = generation.embedding_dimensions
    WHERE generation.activation_state = 'active'
      AND contract.lifecycle_state = 'active'
      AND contract.distance_metric = 'cosine'
    ORDER BY contract.version DESC,
             generation.generation DESC,
             generation.created_at DESC
    LIMIT 1
), retryable AS (
    SELECT job.team_id,
           job.embedding_job_id,
           job.search_document_id
    FROM embedding_jobs AS job
    JOIN search_documents AS document
      ON document.team_id = job.team_id
     AND document.search_document_id = job.search_document_id
     AND document.source_version = job.source_version
     AND document.document_version = job.document_version
     AND document.embedding_contract_id = job.embedding_contract_id
     AND document.embedding_dimensions = job.embedding_dimensions
    JOIN active_contract AS contract
      ON contract.embedding_contract_id = job.embedding_contract_id
     AND contract.dimensions = job.embedding_dimensions
    WHERE job.status = 'failed'
      AND job.attempts = 5
      AND job.max_attempts = 5
      AND document.search_state = 'failed'
      AND (
          lower(job.error) LIKE '%context deadline exceeded%'
          OR lower(job.error) LIKE '%client.timeout exceeded%'
          OR lower(job.error) LIKE '%i/o timeout%'
          OR lower(job.error) LIKE '%rate limit%'
          OR lower(job.error) LIKE '%status 429%'
          OR lower(job.error) LIKE '%connection reset%'
          OR lower(job.error) LIKE '%unexpected eof%'
          OR lower(job.error) LIKE '%provider is unavailable%'
      )
), requeued AS (
    UPDATE embedding_jobs AS job
    SET status = 'queued',
        max_attempts = 20,
        available_at = now(),
        lease_until = NULL,
        worker_id = '',
        completed_at = NULL,
        updated_at = now()
    FROM retryable
    WHERE job.team_id = retryable.team_id
      AND job.embedding_job_id = retryable.embedding_job_id
    RETURNING job.team_id, job.search_document_id
)
UPDATE search_documents AS document
SET search_state = 'pending',
    embedding_error = '',
    updated_at = now()
FROM requeued
WHERE document.team_id = requeued.team_id
  AND document.search_document_id = requeued.search_document_id;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE embedding_jobs
    ALTER COLUMN max_attempts SET DEFAULT 5;

-- +goose StatementEnd
