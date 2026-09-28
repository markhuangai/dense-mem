-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: creates a new table and indexes; the foreign key may
-- briefly lock teams, but existing owner usage rows are not rewritten.
-- RLS impact: FORCE RLS permits system writes and team-scoped reads on the new
-- table; existing owner usage policies are unchanged.
-- Backfill: none; historical owner usage cannot be split among credentials.
-- Backward compatibility: existing owner buckets remain the source for
-- unfiltered totals; credential buckets contain only new attributed usage.
-- A credential ID is retained for 30-day diagnostics even if its credential
-- record is later deleted; its display name is resolved only while present.
SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE usage_credential_buckets (
    bucket_start TIMESTAMPTZ NOT NULL,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    credential_id UUID NOT NULL,
    route TEXT NOT NULL,
    method VARCHAR(16) NOT NULL,
    status_class SMALLINT NOT NULL CHECK (status_class BETWEEN 1 AND 5),
    request_count BIGINT NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    error_count BIGINT NOT NULL DEFAULT 0 CHECK (error_count >= 0),
    mcp_tool_calls BIGINT NOT NULL DEFAULT 0 CHECK (mcp_tool_calls >= 0),
    mcp_tool_failures BIGINT NOT NULL DEFAULT 0 CHECK (mcp_tool_failures >= 0),
    total_latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (total_latency_ms >= 0),
    max_latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (max_latency_ms >= 0),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket_start, team_id, credential_id, route, method, status_class)
);

CREATE INDEX idx_usage_credential_buckets_team_time
    ON usage_credential_buckets(team_id, bucket_start DESC);

CREATE INDEX idx_usage_credential_buckets_credential_time
    ON usage_credential_buckets(credential_id, bucket_start DESC);

ALTER TABLE usage_credential_buckets ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_credential_buckets FORCE ROW LEVEL SECURITY;

CREATE POLICY usage_credential_buckets_system_access ON usage_credential_buckets
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

CREATE POLICY usage_credential_buckets_team_read ON usage_credential_buckets
    FOR SELECT
    USING (
        current_setting('app.tx_mode', true) = 'team'
        AND team_id::text = current_setting('app.current_team_id', true)
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Rollback: Down drops credential-attributed usage collected after this
-- migration; historical owner buckets remain intact.
DROP TABLE usage_credential_buckets;

-- +goose StatementEnd
