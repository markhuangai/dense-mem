-- Lock/rewrite impact: the registered Goose Go executor copies bounded pages in separate transactions and builds membership indexes under the write fence.
-- RLS impact: checkpoint transactions use the migration context established for the copy.
-- Backfill: copies and verifies membership heads, Recall capture, work marker dispositions, and Community work.
-- Backward compatibility: the old layout stays fenced until the verified cutover.
-- Rollback: resume committed checkpoints or restore the verified pre-cutover backup.
-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'topic storage requires the application checkpoint executor'; END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'topic storage requires forward recovery'; END $$;
-- +goose StatementEnd
