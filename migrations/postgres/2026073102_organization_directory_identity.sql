-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

INSERT INTO app_config (key, value)
VALUES
    ('SCIM_PUBLIC_BASE_URL', ''),
    ('CONTROL_PUBLIC_BASE_URL', '')
ON CONFLICT (key) DO NOTHING;

-- Lock/rewrite analysis:
-- - New tables and nullable/defaulted columns are additive. Existing SSO rows
--   are preserved and acquire manual mapping origin/default directory state.
-- - The partial indexes are built transactionally. Schedule the migration in a
--   normal maintenance window for installations with a large identity table.
-- - RLS impact: directory state is system-only; auto-created team mutations are
--   limited to system transactions and marked directory-managed.
-- - Rollback is intentionally blocked once provisioning history exists. Use a
--   forward migration or restore from backup rather than deleting identity data.

ALTER TABLE sso_providers
    ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS identity_claim TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS retired_at TIMESTAMPTZ NULL;

ALTER TABLE sso_identities
    ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;

UPDATE sso_providers
SET identity_claim = 'sub'
WHERE kind = 'azure_ad'
  AND identity_claim = '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_sso_identities_provider_external_id_unique
    ON sso_identities(provider_id, external_id)
    WHERE external_id <> '';

ALTER TABLE sso_group_mappings
    ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'manual',
    ADD COLUMN IF NOT EXISTS retired_at TIMESTAMPTZ NULL;

ALTER TABLE sso_group_mappings
    DROP CONSTRAINT IF EXISTS sso_group_mappings_origin_check;

ALTER TABLE sso_group_mappings
    ADD CONSTRAINT sso_group_mappings_origin_check
    CHECK (origin IN ('manual', 'directory'));

CREATE TABLE IF NOT EXISTS sso_directory_connectors (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id UUID NOT NULL UNIQUE REFERENCES sso_providers(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'disabled' CHECK (status IN ('disabled', 'observe', 'active')),
    group_pattern TEXT NOT NULL,
    role_entitlements JSONB NOT NULL DEFAULT '{}'::jsonb,
    max_auto_teams INTEGER NOT NULL DEFAULT 100 CHECK (max_auto_teams BETWEEN 1 AND 1000),
    credential_version INTEGER NOT NULL DEFAULT 1 CHECK (credential_version >= 1),
    bearer_token_hash TEXT NOT NULL DEFAULT '',
    oauth_client_id TEXT NOT NULL DEFAULT '',
    oauth_client_secret_hash TEXT NOT NULL DEFAULT '',
    last_activation_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (char_length(group_pattern) <= 1024),
    CHECK (jsonb_typeof(role_entitlements) = 'object'),
    CHECK (char_length(bearer_token_hash) <= 512),
    CHECK (char_length(oauth_client_id) <= 128),
    CHECK (char_length(oauth_client_secret_hash) <= 512)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sso_directory_connectors_oauth_client_id_unique
    ON sso_directory_connectors(oauth_client_id)
    WHERE oauth_client_id <> '';

CREATE TABLE IF NOT EXISTS sso_directory_oauth_tokens (
    token_hash TEXT PRIMARY KEY,
    connector_id UUID NOT NULL REFERENCES sso_directory_connectors(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (char_length(token_hash) = 64)
);

CREATE INDEX IF NOT EXISTS idx_sso_directory_oauth_tokens_expires_at
    ON sso_directory_oauth_tokens(expires_at);

CREATE TABLE IF NOT EXISTS sso_directory_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id UUID NOT NULL REFERENCES sso_directory_connectors(id) ON DELETE CASCADE,
    external_id TEXT NOT NULL DEFAULT '',
    user_name TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT true,
    identity_id UUID NOT NULL REFERENCES sso_identities(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connector_id, id),
    CHECK (btrim(user_name) <> ''),
    CHECK (char_length(external_id) <= 512),
    CHECK (char_length(user_name) <= 512),
    CHECK (char_length(email) <= 512),
    CHECK (char_length(display_name) <= 512)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sso_directory_users_connector_external_id_unique
    ON sso_directory_users(connector_id, external_id)
    WHERE external_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_sso_directory_users_connector_username_unique
    ON sso_directory_users(connector_id, lower(user_name));

CREATE TABLE IF NOT EXISTS sso_directory_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id UUID NOT NULL REFERENCES sso_directory_connectors(id) ON DELETE CASCADE,
    external_id TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connector_id, id),
    CHECK (btrim(display_name) <> ''),
    CHECK (char_length(external_id) <= 512),
    CHECK (char_length(display_name) <= 512)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sso_directory_groups_connector_external_id_unique
    ON sso_directory_groups(connector_id, external_id)
    WHERE external_id <> '';

CREATE TABLE IF NOT EXISTS sso_directory_group_memberships (
    connector_id UUID NOT NULL REFERENCES sso_directory_connectors(id) ON DELETE CASCADE,
    group_id UUID NOT NULL,
    user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (connector_id, group_id, user_id),
    FOREIGN KEY (connector_id, group_id)
        REFERENCES sso_directory_groups(connector_id, id) ON DELETE CASCADE,
    FOREIGN KEY (connector_id, user_id)
        REFERENCES sso_directory_users(connector_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sso_directory_group_memberships_user
    ON sso_directory_group_memberships(connector_id, user_id);

CREATE TABLE IF NOT EXISTS sso_directory_group_bindings (
    connector_id UUID NOT NULL REFERENCES sso_directory_connectors(id) ON DELETE CASCADE,
    group_id UUID NOT NULL,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    origin TEXT NOT NULL CHECK (origin IN ('directory_created', 'exact_name', 'adopted')),
    scopes TEXT[] NOT NULL DEFAULT ARRAY['read']::text[],
    role VARCHAR(20) NOT NULL DEFAULT 'member' CHECK (role IN ('manager', 'member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (connector_id, group_id),
    FOREIGN KEY (connector_id, group_id)
        REFERENCES sso_directory_groups(connector_id, id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_sso_directory_group_bindings_team
    ON sso_directory_group_bindings(team_id);

CREATE TABLE IF NOT EXISTS sso_directory_issues (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id UUID NOT NULL REFERENCES sso_directory_connectors(id) ON DELETE CASCADE,
    group_id UUID NULL,
    issue_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('invalid_group', 'ambiguous_group', 'team_collision', 'auto_team_capacity')),
    detail TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connector_id, issue_key),
    FOREIGN KEY (connector_id, group_id)
        REFERENCES sso_directory_groups(connector_id, id) ON DELETE CASCADE,
    CHECK (char_length(issue_key) <= 256),
    CHECK (char_length(detail) <= 1024)
);

ALTER TABLE teams
    ADD COLUMN IF NOT EXISTS directory_connector_id UUID NULL REFERENCES sso_directory_connectors(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS directory_group_id UUID NULL REFERENCES sso_directory_groups(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS directory_managed BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE teams
    DROP CONSTRAINT IF EXISTS teams_directory_managed_shape_check;

ALTER TABLE teams
    ADD CONSTRAINT teams_directory_managed_shape_check
    CHECK (
        (NOT directory_managed AND directory_connector_id IS NULL AND directory_group_id IS NULL)
        OR (directory_managed AND directory_connector_id IS NOT NULL AND directory_group_id IS NOT NULL)
    );

CREATE UNIQUE INDEX IF NOT EXISTS idx_teams_directory_managed_group_unique
    ON teams(directory_connector_id, directory_group_id)
    WHERE directory_managed;

CREATE TABLE IF NOT EXISTS sso_control_admin_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE RESTRICT,
    group_id TEXT NOT NULL CHECK (char_length(group_id) <= 512),
    group_name TEXT NOT NULL DEFAULT '' CHECK (char_length(group_name) <= 512),
    enabled BOOLEAN NOT NULL DEFAULT true,
    retired_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_sso_control_admin_groups_provider_active
    ON sso_control_admin_groups(provider_id, group_id)
    WHERE enabled AND retired_at IS NULL;

CREATE TABLE IF NOT EXISTS sso_control_oauth_states (
    state_hash TEXT PRIMARY KEY CHECK (char_length(state_hash) = 64),
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE RESTRICT,
    pkce_verifier TEXT NOT NULL CHECK (char_length(pkce_verifier) <= 256),
    nonce TEXT NOT NULL CHECK (char_length(nonce) <= 256),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sso_control_oauth_states_expiry
    ON sso_control_oauth_states(expires_at);

CREATE TABLE IF NOT EXISTS sso_control_sessions (
    session_hash TEXT PRIMARY KEY CHECK (char_length(session_hash) = 64),
    identity_id UUID NOT NULL REFERENCES sso_identities(id) ON DELETE RESTRICT,
    provider_id UUID NOT NULL REFERENCES sso_providers(id) ON DELETE RESTRICT,
    group_ids TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    csrf_hash TEXT NOT NULL CHECK (char_length(csrf_hash) = 64),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sso_control_sessions_expiry
    ON sso_control_sessions(expires_at);

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'sso_directory_connectors',
        'sso_directory_oauth_tokens',
        'sso_directory_users',
        'sso_directory_groups',
        'sso_directory_group_memberships',
        'sso_directory_group_bindings',
        'sso_directory_issues',
        'sso_control_admin_groups',
        'sso_control_oauth_states',
        'sso_control_sessions'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_system_access', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR ALL TO PUBLIC USING (current_setting(''app.tx_mode'', true) = ''system'') WITH CHECK (current_setting(''app.tx_mode'', true) = ''system'')',
            table_name || '_system_access',
            table_name
        );
    END LOOP;
END $$;

DROP POLICY IF EXISTS teams_directory_system_insert ON teams;
CREATE POLICY teams_directory_system_insert ON teams
    FOR INSERT
    TO PUBLIC
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'system'
        AND (
            (
                directory_managed
                AND directory_connector_id IS NOT NULL
                AND directory_group_id IS NOT NULL
            )
            OR (
                NOT directory_managed
                AND directory_connector_id IS NULL
                AND directory_group_id IS NULL
            )
        )
    );

DROP POLICY IF EXISTS teams_directory_system_update ON teams;
CREATE POLICY teams_directory_system_update ON teams
    FOR UPDATE
    TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) = 'system'
        AND directory_managed
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'system'
        AND (
            (
                directory_managed
                AND directory_connector_id IS NOT NULL
                AND directory_group_id IS NOT NULL
            )
            OR (
                NOT directory_managed
                AND directory_connector_id IS NULL
                AND directory_group_id IS NULL
            )
        )
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION '2026073102_organization_directory_identity is irreversible once directory state exists; use a forward migration or restore from backup';
END $$;

-- +goose StatementEnd
