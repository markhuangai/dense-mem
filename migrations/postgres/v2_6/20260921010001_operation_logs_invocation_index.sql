-- +goose NO TRANSACTION

-- Runtime Goose providers execute this version through the registered no-transaction Go migration
-- so session settings and concurrent DDL stay on one reserved connection. This SQL remains the
-- migration contract and history source for tooling that inspects the repository files.

-- +goose Up

-- Lock/rewrite impact: every index operation is concurrent; operation_logs is not rewritten and writes remain available.
-- WAL/disk: the derived index is built from retained operation-log rows and consumes index-sized disk and WAL during the build.
-- RLS impact: the index does not alter visibility; operation-log reads continue through the existing system transaction policy.
-- Backfill: none; CREATE INDEX CONCURRENTLY indexes existing rows while preserving new writes.
-- Backward compatibility: older binaries ignore this derived index and retain the same operation-log schema and filters.
-- Recovery: an interrupted concurrent build is renamed when invalid, then dropped before the replacement build.
-- Rollback: the derived index can be dropped without changing operation-log history, retention, or erasure behavior.

SELECT set_config('app.tx_mode', 'migration', true);
SET lock_timeout = '30s';

DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid;

-- CREATE INDEX CONCURRENTLY may leave an invalid catalog entry after an interrupted build.
-- Rename it before the separate concurrent drop so a later migration attempt can recover.
-- +goose StatementBegin
DO $dense_mem_operation_logs_invocation_index_recovery$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS state
        JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname = 'operation_logs_team_invocation_timestamp_idx'
          AND state.indisvalid IS FALSE
    LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I', candidate.relname, candidate.relname || '_invalid');
    END LOOP;
END
$dense_mem_operation_logs_invocation_index_recovery$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid;

CREATE INDEX CONCURRENTLY IF NOT EXISTS operation_logs_team_invocation_timestamp_idx
    ON operation_logs(
        team_id,
        (attrs ->> 'invocation_id'),
        timestamp DESC,
        id DESC
    )
    WHERE (attrs ->> 'invocation_id') IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid;
RESET lock_timeout;

-- +goose Down

SELECT set_config('app.tx_mode', 'migration', true);
SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_team_invocation_timestamp_idx_invalid;
RESET lock_timeout;
