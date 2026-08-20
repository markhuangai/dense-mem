-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: the constant JSONB default is metadata-only on supported
-- PostgreSQL versions. The CHECK is installed NOT VALID, and the partial index
-- is built concurrently so neither operation holds a table-wide scan lock.
-- RLS impact: no policy changes; sso_providers and app_config retain their
-- system-only control-plane policies.
-- Backfill: existing providers receive a disabled config, so OAuth remains
-- dormant until a provider is explicitly enabled. Metadata additionally
-- requires MCP_PUBLIC_BASE_URL.
-- Rollback: down refuses customized state so operator configuration is never
-- silently discarded.

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
    CHECK (jsonb_typeof(protected_resource_config) = 'object') NOT VALID;

-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sso_providers_protected_resource_enabled
    ON sso_providers (id)
    WHERE enabled = true
      AND retired_at IS NULL
      AND protected_resource_config @> '{"enabled": true}'::jsonb;

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'system', true);
INSERT INTO app_config (key, value)
VALUES ('MCP_PUBLIC_BASE_URL', '')
ON CONFLICT (key) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

DO $$
DECLARE
    default_config JSONB := '{
        "enabled": false,
        "audiences": [],
        "jwks_source": "discovery",
        "jwks_uri": "",
        "algorithms": ["RS256"],
        "scope_claim": "scope",
        "scope_mappings": [],
        "team_claim": ""
    }'::jsonb;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM sso_providers
        WHERE protected_resource_config IS DISTINCT FROM default_config
    ) THEN
        RAISE EXCEPTION 'refusing OAuth protected-resource rollback with customized provider configuration';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM app_config
        WHERE key = 'MCP_PUBLIC_BASE_URL' AND value <> ''
    ) THEN
        RAISE EXCEPTION 'refusing OAuth protected-resource rollback with customized MCP_PUBLIC_BASE_URL';
    END IF;
END $$;

-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS idx_sso_providers_protected_resource_enabled;

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'system', true);
DELETE FROM app_config WHERE key = 'MCP_PUBLIC_BASE_URL' AND value = '';
ALTER TABLE sso_providers DROP CONSTRAINT IF EXISTS sso_providers_protected_resource_object;
ALTER TABLE sso_providers DROP COLUMN IF EXISTS protected_resource_config;

-- +goose StatementEnd
