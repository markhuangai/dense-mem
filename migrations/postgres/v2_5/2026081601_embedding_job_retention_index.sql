-- +goose NO TRANSACTION

-- +goose Up
-- Lock/rewrite impact: the partial index is built concurrently; embedding_jobs is not rewritten.
-- WAL/disk: the build writes one bounded index entry per terminal job with completed_at.
-- RLS impact: the index does not change visibility; the retention worker still uses system context.
-- Backfill: none; existing terminal jobs enter the partial index during the concurrent build.
-- Backward compatibility: existing workers continue to use embedding_jobs without query changes.
-- Recovery: an invalid interrupted build is renamed, rebuilt, and removed after success.
-- Rollback: the index is derived state and can be dropped without changing job history.

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_terminal_retention_invalid_idx;

-- +goose StatementBegin
DO $dense_mem_embedding_job_retention_invalid_index$
DECLARE
    index_valid BOOLEAN;
BEGIN
    SELECT state.indisvalid
    INTO index_valid
    FROM pg_index AS state
    JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
    JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
    WHERE namespace.nspname = 'public'
      AND index_class.relname = 'embedding_jobs_terminal_retention_idx';

    IF index_valid IS FALSE THEN
        ALTER INDEX public.embedding_jobs_terminal_retention_idx
            RENAME TO embedding_jobs_terminal_retention_invalid_idx;
    END IF;
END;
$dense_mem_embedding_job_retention_invalid_index$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS embedding_jobs_terminal_retention_idx
    ON embedding_jobs(completed_at, team_id, embedding_job_id)
    WHERE status IN ('completed', 'stale', 'cancelled')
      AND completed_at IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_terminal_retention_invalid_idx;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_terminal_retention_idx;
DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_terminal_retention_invalid_idx;
