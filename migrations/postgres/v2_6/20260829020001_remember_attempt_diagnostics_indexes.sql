-- +goose NO TRANSACTION

-- +goose Up

-- Lock/rewrite impact: both indexes are built concurrently, so Remember
-- attempt writes remain available and no heap rewrite is performed.
-- WAL/disk: each existing attempt contributes one entry to the ordered index
-- and one entry to the outcome-filtered index.
-- RLS impact: indexes do not change visibility; diagnostics retain system RLS
-- reads and the application still binds the requested team and outcome.
-- Backfill: none; the concurrent builds cover existing append-only attempts.
-- Backward compatibility: older binaries ignore these derived indexes.
-- Rollback: these indexes are derived pagination state and can be dropped
-- without changing attempt, event, or artifact history.

DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_outcome_created_idx_invalid;

-- CREATE INDEX CONCURRENTLY may leave an invalid catalog entry after an
-- interrupted build. Rename invalid entries so the replacement can be built.
-- +goose StatementBegin
DO $dense_mem_remember_attempt_diagnostics_invalid_indexes$
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
              'remember_attempts_diagnostics_created_idx',
              'remember_attempts_diagnostics_outcome_created_idx'
          )
          AND state.indisvalid IS FALSE
    LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I', candidate.relname, candidate.relname || '_invalid');
    END LOOP;
END
$dense_mem_remember_attempt_diagnostics_invalid_indexes$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_diagnostics_created_idx
    ON remember_attempts(team_id, created_at DESC, attempt_id DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_diagnostics_outcome_created_idx
    ON remember_attempts(team_id, outcome, created_at DESC, attempt_id DESC);

DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_outcome_created_idx_invalid;

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_outcome_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_outcome_created_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_diagnostics_created_idx_invalid;
