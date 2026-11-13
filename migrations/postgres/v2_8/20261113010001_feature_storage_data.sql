-- Lock/rewrite impact: application-owned bounded transactions; canonical tables are read only.
-- RLS impact: explicit migration reads and migration-only checkpoint/payload writes.
-- Backfill: at most 100 rows / 8 MiB per transaction, with source-bound checkpoints.
-- Backward compatibility: startup remains fenced until cutover validates the new layout.
-- Rollback: rerun the application to verify retained prefixes and resume committed batches.

-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'feature storage data migration requires the application checkpoint executor'; END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'feature storage recovery rolls forward'; END $$;
-- +goose StatementEnd
