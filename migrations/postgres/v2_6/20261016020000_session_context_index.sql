-- +goose NO TRANSACTION

-- Lock/rewrite impact: concurrent index creation preserves writes and does not rewrite intake.
-- RLS impact: fixed-size identity hashes narrow lookup; exact identity and authorization predicates remain required.
-- Backfill/recovery: index existing completed rows; repair an interrupted invalid index before retry.
-- Backward compatibility: previous releases retain their existing exact lookup and context index.
-- Rollback: drop only the derived index; retain intake, diagnostics, and the previous context index.

-- +goose Up
SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS session_events_session_context_idx_invalid;
-- +goose StatementBegin
DO $repair$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_index AS state JOIN pg_class AS name ON name.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = name.relnamespace
        WHERE namespace.nspname = 'public' AND name.relname = 'session_events_session_context_idx' AND NOT state.indisvalid) THEN
        ALTER INDEX session_events_session_context_idx RENAME TO session_events_session_context_idx_invalid;
    END IF;
END $repair$;
-- +goose StatementEnd
DROP INDEX CONCURRENTLY IF EXISTS session_events_session_context_idx_invalid;
CREATE INDEX CONCURRENTLY IF NOT EXISTS session_events_session_context_idx ON session_events
    (team_id, space_id, owner_profile_id, space_generation,
     md5(framework), md5(app_name), md5(user_id), md5(session_id), created_at DESC, event_index DESC)
    WHERE result IS NOT NULL;
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS session_events_session_context_idx;
RESET lock_timeout;
