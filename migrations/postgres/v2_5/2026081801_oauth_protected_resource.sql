-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: the additive JSONB column is metadata-only on supported
-- PostgreSQL versions. CHECK validation and the partial index scan the small
-- control-plane provider table while their DDL locks are held.
-- RLS impact: no policy changes; sso_providers and app_config retain their
-- existing system-only control-plane policies.
-- Backfill: existing providers receive a disabled protected-resource object;
-- no existing authentication behavior changes until control enables it.
-- Backward compatibility: old binaries ignore the additive JSONB column and
-- continue to treat every provider as browser-login configuration only.
-- Rollback: disabling every protected-resource config is the operational
-- rollback. The down migration removes only the dormant additive fields.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

ALTER TABLE sso_providers
    ADD COLUMN IF NOT EXISTS protected_resource_config JSONB NOT NULL DEFAULT '{
        "enabled": false,
        "audiences": [],
        "jwks_source": "discovery",
        "jwks_uri": "",
        "algorithms": ["RS256"],
        "scope_claim": "scope",
        "scope_mappings": [],
        "team_claim": ""
    }'::jsonb;

ALTER TABLE sso_providers
    DROP CONSTRAINT IF EXISTS sso_providers_protected_resource_object;
ALTER TABLE sso_providers
    ADD CONSTRAINT sso_providers_protected_resource_object
    CHECK (jsonb_typeof(protected_resource_config) = 'object');

CREATE INDEX IF NOT EXISTS idx_sso_providers_protected_resource_enabled
    ON sso_providers (id)
    WHERE enabled = true
      AND retired_at IS NULL
      AND protected_resource_config @> '{"enabled": true}'::jsonb;

SELECT set_config('app.tx_mode', 'system', true);
INSERT INTO app_config (key, value)
VALUES ('MCP_PUBLIC_BASE_URL', '')
ON CONFLICT (key) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DELETE FROM app_config WHERE key = 'MCP_PUBLIC_BASE_URL' AND value = '';
DROP INDEX IF EXISTS idx_sso_providers_protected_resource_enabled;
ALTER TABLE sso_providers DROP CONSTRAINT IF EXISTS sso_providers_protected_resource_object;
ALTER TABLE sso_providers DROP COLUMN IF EXISTS protected_resource_config;

-- +goose StatementEnd
