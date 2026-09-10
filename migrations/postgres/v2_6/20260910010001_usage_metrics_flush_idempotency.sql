-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: creates one small additive table and index; existing
-- usage_metric_buckets rows are not rewritten.
-- RLS impact: flush identifiers are system-only control state, matching the
-- system transaction policy used for usage metrics.
-- Backfill: none; only flushes created after this migration need deduplication.
-- Backward compatibility: existing usage_metric_buckets rows and dimensions
-- remain unchanged; the repository starts supplying a flush identifier.
-- Rollback: drop the control table only after usage-metric retry traffic is
-- stopped; removing it would reopen duplicate-counting on retries.

-- Additive control state for usage-metric flush retries. The table and index
-- do not rewrite usage_metric_buckets; rows are pruned with metric retention.
CREATE TABLE IF NOT EXISTS usage_metric_flushes (
    flush_id UUID PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_metric_flushes_created_at
    ON usage_metric_flushes(created_at);

ALTER TABLE usage_metric_flushes ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_metric_flushes FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS usage_metric_flushes_system_access ON usage_metric_flushes;
CREATE POLICY usage_metric_flushes_system_access ON usage_metric_flushes
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP POLICY IF EXISTS usage_metric_flushes_system_access ON usage_metric_flushes;
DROP TABLE IF EXISTS usage_metric_flushes;

-- +goose StatementEnd
