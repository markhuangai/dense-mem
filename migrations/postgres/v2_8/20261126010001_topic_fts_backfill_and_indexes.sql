-- Lock/rewrite impact: the registered no-transaction executor updates bounded pages and builds indexes concurrently.
-- RLS impact: backfill runs in migration mode and preserves scoped derived tables.
-- Backfill: missing Topic search vectors and historical equivalence statuses are resumable in 1,000-row pages.
-- Backward compatibility: the schema remains additive and existing Topic history is preserved.
-- Rollback: restore the verified offline backup or use forward recovery after writes reopen.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Topic hybrid backfill requires the application executor'; END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Topic hybrid backfill requires forward recovery'; END $$;
-- +goose StatementEnd
