-- Lock/rewrite impact: the Goose data executor copies and verifies bounded pages.
-- RLS impact: migration transactions populate the new tables under their existing scope.
-- Backfill: committed pages are resumable through durable copy and verification checkpoints.
-- Backward compatibility: the legacy tables and write fence remain through cutover.
-- Rollback: resume from committed checkpoints or restore the verified backup with services offline.

-- +goose Up
SELECT 'relational_topics_v1'::text;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'offline topic upgrade requires forward recovery or backup restoration'; END $$;
-- +goose StatementEnd
