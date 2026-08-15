-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS sso_providers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    kind VARCHAR(32) NOT NULL CHECK (kind IN ('azure_ad', 'pingone', 'generic_oidc')),
    issuer_url TEXT NOT NULL,
    client_id TEXT NOT NULL,
    client_secret_env TEXT NOT NULL DEFAULT '',
    scopes TEXT[] NOT NULL DEFAULT ARRAY['openid','profile','email']::text[],
    group_claims TEXT[] NOT NULL DEFAULT ARRAY['groups']::text[],
    groups_endpoint TEXT NOT NULL DEFAULT '',
    groups_scopes TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sso_providers_name_unique
    ON sso_providers (lower(name));

CREATE TABLE IF NOT EXISTS sso_identities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
    subject TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    last_login_at TIMESTAMPTZ NULL,
    last_entitlement_check_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_id, subject)
);

CREATE INDEX IF NOT EXISTS idx_sso_identities_provider_subject
    ON sso_identities(provider_id, subject);

CREATE TABLE IF NOT EXISTS sso_group_mappings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL,
    group_name TEXT NOT NULL DEFAULT '',
    scopes TEXT[] NOT NULL DEFAULT ARRAY['read']::text[],
    role VARCHAR(20) NOT NULL DEFAULT 'member' CHECK (role IN ('manager', 'member')),
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_id, team_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_sso_group_mappings_provider_group
    ON sso_group_mappings(provider_id, group_id)
    WHERE enabled = true;

CREATE TABLE IF NOT EXISTS sso_entitlement_cache (
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
    subject TEXT NOT NULL,
    groups TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    status VARCHAR(20) NOT NULL CHECK (status IN ('active', 'denied', 'error')),
    checked_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider_id, subject)
);

CREATE TABLE IF NOT EXISTS sso_oauth_states (
    state_hash TEXT PRIMARY KEY,
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
    pkce_verifier TEXT NOT NULL,
    nonce TEXT NOT NULL,
    redirect_path TEXT NOT NULL DEFAULT '/ui',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sso_oauth_states_expires_at
    ON sso_oauth_states(expires_at);

CREATE TABLE IF NOT EXISTS sso_sessions (
    session_hash TEXT PRIMARY KEY,
    identity_id UUID NOT NULL REFERENCES sso_identities(id) ON DELETE CASCADE,
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE CASCADE,
    team_profile_id UUID NOT NULL REFERENCES team_profiles(id) ON DELETE CASCADE,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    csrf_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sso_sessions_identity_id
    ON sso_sessions(identity_id);

CREATE INDEX IF NOT EXISTS idx_sso_sessions_expires_at
    ON sso_sessions(expires_at);

ALTER TABLE team_profiles
    ALTER COLUMN key_hash DROP NOT NULL,
    ALTER COLUMN key_prefix DROP NOT NULL;

DROP INDEX IF EXISTS idx_team_profiles_key_prefix_unique;
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_key_prefix_unique
    ON team_profiles(key_prefix)
    WHERE key_prefix IS NOT NULL;

ALTER TABLE team_profiles
    ADD COLUMN IF NOT EXISTS auth_source VARCHAR(20) NOT NULL DEFAULT 'api_key',
    ADD COLUMN IF NOT EXISTS sso_identity_id UUID NULL REFERENCES sso_identities(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS sso_provider_id UUID NULL REFERENCES sso_providers(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS sso_subject TEXT NULL,
    ADD COLUMN IF NOT EXISTS sso_email TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sso_group_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sso_entitlement_status VARCHAR(20) NOT NULL DEFAULT 'unlinked',
    ADD COLUMN IF NOT EXISTS sso_last_entitlement_checked_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS sso_last_login_at TIMESTAMPTZ NULL;

ALTER TABLE team_profiles
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_check,
    DROP CONSTRAINT IF EXISTS team_profiles_sso_entitlement_status_check,
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_shape_check;

ALTER TABLE team_profiles
    ADD CONSTRAINT team_profiles_auth_source_check
        CHECK (auth_source IN ('api_key', 'sso')),
    ADD CONSTRAINT team_profiles_sso_entitlement_status_check
        CHECK (sso_entitlement_status IN ('unlinked', 'active', 'denied', 'error')),
    ADD CONSTRAINT team_profiles_auth_source_shape_check
        CHECK (
            (
                auth_source = 'api_key'
                AND key_hash IS NOT NULL
                AND key_prefix IS NOT NULL
                AND sso_identity_id IS NULL
                AND sso_provider_id IS NULL
                AND NULLIF(sso_subject, '') IS NULL
                AND sso_entitlement_status = 'unlinked'
            )
            OR (
                auth_source = 'sso'
                AND key_hash IS NULL
                AND key_prefix IS NULL
                AND NULLIF(sso_subject, '') IS NOT NULL
                AND sso_entitlement_status IN ('active', 'denied', 'error')
                AND (
                    (sso_identity_id IS NOT NULL AND sso_provider_id IS NOT NULL)
                    OR (sso_identity_id IS NULL AND sso_provider_id IS NULL)
                )
            )
        );

CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_sso_identity_team_unique
    ON team_profiles(sso_identity_id, team_id)
    WHERE sso_identity_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_team_profiles_sso_provider_subject
    ON team_profiles(sso_provider_id, sso_subject)
    WHERE sso_provider_id IS NOT NULL AND sso_subject IS NOT NULL;

DO $$
BEGIN
    IF to_regclass('public.sso_providers') IS NOT NULL THEN
        ALTER TABLE sso_providers ENABLE ROW LEVEL SECURITY;
        ALTER TABLE sso_providers FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS sso_providers_system_access ON sso_providers;
        CREATE POLICY sso_providers_system_access ON sso_providers
            FOR ALL
            TO PUBLIC
            USING (current_setting('app.tx_mode', true) = 'system')
            WITH CHECK (current_setting('app.tx_mode', true) = 'system');
    END IF;

    IF to_regclass('public.sso_identities') IS NOT NULL THEN
        ALTER TABLE sso_identities ENABLE ROW LEVEL SECURITY;
        ALTER TABLE sso_identities FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS sso_identities_system_access ON sso_identities;
        CREATE POLICY sso_identities_system_access ON sso_identities
            FOR ALL
            TO PUBLIC
            USING (current_setting('app.tx_mode', true) = 'system')
            WITH CHECK (current_setting('app.tx_mode', true) = 'system');
    END IF;

    IF to_regclass('public.sso_group_mappings') IS NOT NULL THEN
        ALTER TABLE sso_group_mappings ENABLE ROW LEVEL SECURITY;
        ALTER TABLE sso_group_mappings FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS sso_group_mappings_system_access ON sso_group_mappings;
        DROP POLICY IF EXISTS sso_group_mappings_team_read ON sso_group_mappings;

        CREATE POLICY sso_group_mappings_system_access ON sso_group_mappings
            FOR ALL
            TO PUBLIC
            USING (current_setting('app.tx_mode', true) = 'system')
            WITH CHECK (current_setting('app.tx_mode', true) = 'system');

        CREATE POLICY sso_group_mappings_team_read ON sso_group_mappings
            FOR SELECT
            TO PUBLIC
            USING (
                team_id = COALESCE(
                    nullif(current_setting('app.current_team_id', true), '')::uuid,
                    nullif(current_setting('app.current_profile_id', true), '')::uuid
                )
            );
    END IF;

    IF to_regclass('public.sso_entitlement_cache') IS NOT NULL THEN
        ALTER TABLE sso_entitlement_cache ENABLE ROW LEVEL SECURITY;
        ALTER TABLE sso_entitlement_cache FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS sso_entitlement_cache_system_access ON sso_entitlement_cache;
        CREATE POLICY sso_entitlement_cache_system_access ON sso_entitlement_cache
            FOR ALL
            TO PUBLIC
            USING (current_setting('app.tx_mode', true) = 'system')
            WITH CHECK (current_setting('app.tx_mode', true) = 'system');
    END IF;

    IF to_regclass('public.sso_oauth_states') IS NOT NULL THEN
        ALTER TABLE sso_oauth_states ENABLE ROW LEVEL SECURITY;
        ALTER TABLE sso_oauth_states FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS sso_oauth_states_system_access ON sso_oauth_states;
        CREATE POLICY sso_oauth_states_system_access ON sso_oauth_states
            FOR ALL
            TO PUBLIC
            USING (current_setting('app.tx_mode', true) = 'system')
            WITH CHECK (current_setting('app.tx_mode', true) = 'system');
    END IF;

    IF to_regclass('public.sso_sessions') IS NOT NULL THEN
        ALTER TABLE sso_sessions ENABLE ROW LEVEL SECURITY;
        ALTER TABLE sso_sessions FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS sso_sessions_system_access ON sso_sessions;
        CREATE POLICY sso_sessions_system_access ON sso_sessions
            FOR ALL
            TO PUBLIC
            USING (current_setting('app.tx_mode', true) = 'system')
            WITH CHECK (current_setting('app.tx_mode', true) = 'system');
    END IF;
END $$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS sso_sessions;
DROP TABLE IF EXISTS sso_oauth_states;
DROP TABLE IF EXISTS sso_entitlement_cache;

DROP INDEX IF EXISTS idx_team_profiles_sso_provider_subject;
DROP INDEX IF EXISTS idx_team_profiles_sso_identity_team_unique;

ALTER TABLE team_profiles
    DROP CONSTRAINT IF EXISTS team_profiles_sso_entitlement_status_check,
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_shape_check,
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_check;

DELETE FROM team_profiles WHERE auth_source = 'sso';

ALTER TABLE team_profiles
    DROP COLUMN IF EXISTS sso_last_login_at,
    DROP COLUMN IF EXISTS sso_last_entitlement_checked_at,
    DROP COLUMN IF EXISTS sso_entitlement_status,
    DROP COLUMN IF EXISTS sso_group_id,
    DROP COLUMN IF EXISTS sso_email,
    DROP COLUMN IF EXISTS sso_subject,
    DROP COLUMN IF EXISTS sso_provider_id,
    DROP COLUMN IF EXISTS sso_identity_id,
    DROP COLUMN IF EXISTS auth_source;

ALTER TABLE team_profiles
    ALTER COLUMN key_hash SET NOT NULL,
    ALTER COLUMN key_prefix SET NOT NULL;

DROP TABLE IF EXISTS sso_group_mappings;
DROP TABLE IF EXISTS sso_identities;
DROP TABLE IF EXISTS sso_providers;

DROP INDEX IF EXISTS idx_team_profiles_key_prefix_unique;
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_key_prefix_unique
    ON team_profiles(key_prefix);

ALTER TABLE team_profiles
    ALTER COLUMN key_hash SET NOT NULL,
    ALTER COLUMN key_prefix SET NOT NULL;

-- +goose StatementEnd
