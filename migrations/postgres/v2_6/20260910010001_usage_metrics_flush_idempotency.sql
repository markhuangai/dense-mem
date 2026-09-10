-- +goose Up
-- +goose StatementBegin

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
