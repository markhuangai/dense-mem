-- +goose NO TRANSACTION

-- +goose Up

-- Issue #318 activates Remember retry/replay semantics. The retry index is
-- rebuilt forward-only so legacy NULL retryability remains effectively true
-- while completed and explicitly non-retryable failures stay out of the
-- retryable lookup path. CREATE INDEX CONCURRENTLY may leave an invalid
-- catalog entry when interrupted; normalize that entry before every retry.
-- Historical attempt rows and outcomes are retained; this migration changes
-- only the derived lookup index.
-- Lock/rewrite impact: all index operations are concurrent; no heap rewrite is
-- performed, and writes remain available while the derived index is rebuilt.
-- RLS impact: the index does not alter visibility; Remember attempt reads still
-- execute through the existing team/profile transaction context.
-- Backfill: none; the concurrent replacement indexes cover existing attempts.
-- Backward compatibility: older binaries ignore this derived lookup index;
-- nullable retryability continues to be interpreted by outcome until cutover.
-- Rollback: the index is derived state; the migration is forward-only and a
-- verified snapshot is required to restore a prior catalog definition.
SELECT set_config('app.tx_mode', 'migration', true);
-- NO TRANSACTION migrations need a session-scoped timeout for every DDL statement.
SET lock_timeout = '30s';

DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_failed_retryable_idx_invalid;

-- Rename an interrupted canonical build before dropping it. PostgreSQL does
-- not allow DROP INDEX CONCURRENTLY inside the same transaction as a rename,
-- hence the separate statements surrounding this block.

-- +goose StatementBegin
DO $dense_mem_retry_index_recovery$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS state
        JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname = 'remember_attempts_failed_retryable_idx'
          AND state.indisvalid IS FALSE
    LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I', candidate.relname, candidate.relname || '_invalid');
    END LOOP;
END
$dense_mem_retry_index_recovery$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_failed_retryable_idx;
DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_failed_retryable_idx_invalid;

CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_failed_retryable_idx
    ON remember_attempts(team_id, owner_profile_id, idempotency_key, created_at DESC, attempt_id DESC)
    WHERE outcome = 'failed' AND COALESCE(retryable, true);

DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_failed_retryable_idx_invalid;

-- Return the Goose connection to the application's default session settings.
RESET lock_timeout;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'Remember retry activation is append-only; restore a verified snapshot to roll back';
END;
$$;
-- +goose StatementEnd
