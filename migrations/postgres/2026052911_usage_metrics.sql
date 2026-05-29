-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS usage_metric_buckets (
    bucket_start TIMESTAMPTZ NOT NULL,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    key_id UUID NOT NULL REFERENCES team_profiles(id) ON DELETE CASCADE,
    route TEXT NOT NULL,
    method VARCHAR(16) NOT NULL,
    status_class SMALLINT NOT NULL CHECK (status_class BETWEEN 1 AND 5),
    request_count BIGINT NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    error_count BIGINT NOT NULL DEFAULT 0 CHECK (error_count >= 0),
    total_latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (total_latency_ms >= 0),
    max_latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (max_latency_ms >= 0),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket_start, team_id, key_id, route, method, status_class)
);

CREATE INDEX IF NOT EXISTS idx_usage_metric_buckets_bucket_start
    ON usage_metric_buckets(bucket_start DESC);

CREATE INDEX IF NOT EXISTS idx_usage_metric_buckets_team_bucket
    ON usage_metric_buckets(team_id, bucket_start DESC);

CREATE INDEX IF NOT EXISTS idx_usage_metric_buckets_key_bucket
    ON usage_metric_buckets(key_id, bucket_start DESC);

ALTER TABLE usage_metric_buckets ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_metric_buckets FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS usage_metric_buckets_system_access ON usage_metric_buckets;
CREATE POLICY usage_metric_buckets_system_access ON usage_metric_buckets
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

DROP POLICY IF EXISTS usage_metric_buckets_team_read ON usage_metric_buckets;
CREATE POLICY usage_metric_buckets_team_read ON usage_metric_buckets
    FOR SELECT
    USING (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id::text = current_setting('app.current_team_id', true)
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP POLICY IF EXISTS usage_metric_buckets_team_read ON usage_metric_buckets;
DROP POLICY IF EXISTS usage_metric_buckets_system_access ON usage_metric_buckets;
DROP TABLE IF EXISTS usage_metric_buckets;

-- +goose StatementEnd
