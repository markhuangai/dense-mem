-- +goose NO TRANSACTION

-- +goose Up

-- Lock/rewrite impact: every index is built concurrently; neither placement_runs nor operation_logs is rewritten.
-- WAL/disk: builds add bounded index entries for existing placement runs and retained operation logs.
-- RLS impact: indexes do not change visibility; diagnostics continue to use system transaction context.
-- Backfill: none; existing rows enter each index during its concurrent build.
-- Backward compatibility: application and worker writes are unchanged while older images ignore these indexes.
-- Recovery: invalid interrupted builds are renamed, rebuilt, and removed after the replacement is valid.
-- Rollback: all four indexes are derived state and can be dropped without changing submission or log history.

DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_team_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_event_timestamp_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_reference_timestamp_idx_invalid;

-- +goose StatementBegin
DO $dense_mem_submission_diagnostics_invalid_indexes$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS state
        JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname IN (
              'placement_runs_control_created_idx',
              'placement_runs_control_team_created_idx',
              'operation_logs_event_timestamp_idx',
              'operation_logs_reference_timestamp_idx'
          )
          AND state.indisvalid IS FALSE
    LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I', candidate.relname, candidate.relname || '_invalid');
    END LOOP;
END;
$dense_mem_submission_diagnostics_invalid_indexes$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS placement_runs_control_created_idx
    ON placement_runs(created_at DESC, placement_run_id DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS placement_runs_control_team_created_idx
    ON placement_runs(team_id, created_at DESC, placement_run_id DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS operation_logs_event_timestamp_idx
    ON operation_logs(message, timestamp DESC, id DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS operation_logs_reference_timestamp_idx
    ON operation_logs(
        (attrs ->> 'reference_type'),
        (attrs ->> 'reference_id'),
        timestamp DESC,
        id DESC
    )
    WHERE attrs ? 'reference_type' AND attrs ? 'reference_id';

DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_team_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_event_timestamp_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_reference_timestamp_idx_invalid;

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS operation_logs_reference_timestamp_idx;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_event_timestamp_idx;
DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_team_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS placement_runs_control_team_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_event_timestamp_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS operation_logs_reference_timestamp_idx_invalid;
