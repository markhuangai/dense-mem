-- Lock/rewrite impact: adds one nullable timestamp column on topic_maintenance_teams and builds derived operational indexes concurrently without table rewrites.
-- RLS impact: preserves existing forced RLS policies on all topic_maintenance_* tables.
-- Backfill: none; historical last_successful_progress stays in topic_maintenance_state and status reads merge both values.
-- Backward compatibility: additive column and derived partial indexes leave existing readers and historical rows intact.
-- Rollback: forward recovery; derived indexes can be dropped independently without altering durable state.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Topic maintenance index migration requires the application executor'; END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'topic maintenance index migration requires forward recovery'; END $$;
-- +goose StatementEnd
