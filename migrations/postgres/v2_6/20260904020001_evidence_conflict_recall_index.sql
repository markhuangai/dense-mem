-- Lock/rewrite impact: one concurrent derived index is added; no heap rows are
-- rewritten and normal reads/writes remain available during the build.
-- RLS impact: the index does not change visibility or transaction-local policy.
-- Backfill: existing event rows are covered by the concurrent index build.
-- Backward compatibility: older binaries continue using the existing history
-- index, while this lookup becomes bounded for historical Recall.
-- Rollback: the derived index is forward-only; restoring a verified catalog
-- snapshot is required to remove it after deployment.

-- +goose NO TRANSACTION

-- +goose Up

SELECT set_config('app.tx_mode', 'migration', true);
SET lock_timeout = '30s';

CREATE INDEX CONCURRENTLY IF NOT EXISTS evidence_conflict_events_created_at_idx
    ON evidence_conflict_events(team_id, conflict_id, created_at DESC, ordinal DESC, conflict_event_id DESC);

RESET lock_timeout;

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION 'Evidence conflict Recall timestamp index is derived state; restore a verified catalog snapshot to roll back';
END;
$$;

-- +goose StatementEnd
