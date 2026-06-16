-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS app_config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

WITH seed_update AS (
    SELECT regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ) AS value
)
INSERT INTO app_config (key, value)
VALUES
    ('update_time', (SELECT value FROM seed_update)),
    ('SSO_PUBLIC_BASE_URL', ''),
    ('SSO_ENTITLEMENT_CACHE_TTL_SECONDS', ''),
    ('SSO_SESSION_TTL_SECONDS', ''),
    ('SSO_STATE_TTL_SECONDS', ''),
    ('SSO_HTTP_TIMEOUT_SECONDS', ''),
    ('SSO_COOKIE_SECURE', ''),
    ('APP_TIMEZONE', 'Local')
ON CONFLICT (key) DO NOTHING;

ALTER TABLE app_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_config FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS app_config_system_access ON app_config;
CREATE POLICY app_config_system_access ON app_config
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS app_config;

-- +goose StatementEnd
