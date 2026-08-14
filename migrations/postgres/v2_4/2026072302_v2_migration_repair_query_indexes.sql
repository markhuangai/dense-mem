-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Lock/rewrite: each CREATE INDEX holds a SHARE lock and blocks writes for its scan; no heap rewrite occurs.
-- WAL/disk: temporary and durable index space is proportional to the indexed migration/placement rows.
-- RLS/roles: indexes do not change policies or grants and are used under existing migration-mode RLS.
-- Rollback: the down migration drops only these derived indexes and does not modify canonical data.
CREATE INDEX IF NOT EXISTS v2_migration_corpus_run_ingest_idx
    ON v2_migration_corpus_items(run_id, team_id, ingest_id)
    WHERE ingest_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS placement_items_migration_ingest_idx
    ON placement_items(team_id, ingest_id)
    WHERE evidence_index = 0;

CREATE INDEX IF NOT EXISTS review_tasks_open_placement_item_idx
    ON review_tasks(team_id, placement_item_id)
    WHERE placement_item_id IS NOT NULL
      AND status IN ('open', 'acknowledged');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS review_tasks_open_placement_item_idx;
DROP INDEX IF EXISTS placement_items_migration_ingest_idx;
DROP INDEX IF EXISTS v2_migration_corpus_run_ingest_idx;

-- +goose StatementEnd
