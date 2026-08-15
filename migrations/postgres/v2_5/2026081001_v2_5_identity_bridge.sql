-- +goose Up
-- +goose StatementBegin

-- Dense-Mem v2.5 expands the legacy team_profiles authority without changing
-- any legacy identifiers. The bridge is additive and remains active until a
-- later, explicitly gated cleanup release.
-- Lock/rewrite impact: additive tables and indexes; no legacy table rewrite.
-- RLS impact: every new relation is FORCE RLS with team/system contexts.
-- Backfill: idempotent profile-to-identity reconciliation runs in this transaction.
-- Backward compatibility: legacy team_profiles reads/writes remain supported by a trigger.
-- Rollback: local Down removes only bridge objects; production uses roll-forward/PITR.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS actor_identities (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('human', 'api_client', 'system')),
    team_id UUID NULL REFERENCES teams(id) ON DELETE RESTRICT,
    provider TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (char_length(provider) <= 128),
    CHECK (char_length(subject) <= 512),
    CHECK (char_length(display_name) <= 512)
);

CREATE INDEX IF NOT EXISTS idx_actor_identities_team_active
    ON actor_identities(team_id, active);
CREATE UNIQUE INDEX IF NOT EXISTS idx_actor_identities_provider_subject
    ON actor_identities(provider, subject)
    WHERE provider <> '' AND subject <> '';

CREATE TABLE IF NOT EXISTS team_memberships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_identity_id UUID NOT NULL REFERENCES actor_identities(id) ON DELETE RESTRICT,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'revoked')),
    team_admin BOOLEAN NOT NULL DEFAULT false,
    maximum_grants TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    legacy_profile_id UUID NULL REFERENCES team_profiles(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (actor_identity_id, team_id),
    CHECK (cardinality(maximum_grants) IS NULL OR cardinality(maximum_grants) <= 128)
);

CREATE INDEX IF NOT EXISTS idx_team_memberships_team_status
    ON team_memberships(team_id, status, team_admin);
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_memberships_legacy_profile
    ON team_memberships(legacy_profile_id)
    WHERE legacy_profile_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS credentials (
    id UUID PRIMARY KEY,
    actor_identity_id UUID NOT NULL REFERENCES actor_identities(id) ON DELETE RESTRICT,
    owner_identity_id UUID NULL REFERENCES actor_identities(id) ON DELETE SET NULL,
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL DEFAULT 'api_key' CHECK (kind IN ('api_key', 'oauth', 'session', 'system')),
    key_hash TEXT NULL,
    key_prefix VARCHAR(24) NULL,
    key_suffix VARCHAR(6) NULL,
    name VARCHAR(100) NOT NULL DEFAULT '',
    scopes TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    rate_limit INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit >= 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'expired', 'disabled')),
    expires_at TIMESTAMPTZ NULL,
    revoked_at TIMESTAMPTZ NULL,
    legacy_profile_id UUID NULL REFERENCES team_profiles(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ NULL,
    CHECK (kind <> 'api_key' OR (key_hash IS NOT NULL AND key_prefix IS NOT NULL)),
    CHECK (char_length(name) <= 100),
    CHECK (cardinality(scopes) IS NULL OR cardinality(scopes) <= 128)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_key_prefix_unique
    ON credentials(key_prefix)
    WHERE key_prefix IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_legacy_profile
    ON credentials(legacy_profile_id)
    WHERE legacy_profile_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_credentials_owner_identity
    ON credentials(owner_identity_id)
    WHERE owner_identity_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_credentials_team_status
    ON credentials(team_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS membership_grants (
    membership_id UUID NOT NULL REFERENCES team_memberships(id) ON DELETE CASCADE,
    grant_name TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'legacy_scope' CHECK (source IN ('legacy_scope', 'explicit', 'system')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (membership_id, grant_name),
    CHECK (char_length(grant_name) BETWEEN 1 AND 128)
);

CREATE TABLE IF NOT EXISTS identity_external_links (
    identity_id UUID NOT NULL REFERENCES actor_identities(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    external_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, external_id),
    UNIQUE (identity_id, provider),
    CHECK (char_length(provider) BETWEEN 1 AND 128),
    CHECK (char_length(external_id) BETWEEN 1 AND 512)
);

CREATE TABLE IF NOT EXISTS ownership_aliases (
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    legacy_owner_id UUID NOT NULL,
    canonical_identity_id UUID NOT NULL REFERENCES actor_identities(id) ON DELETE RESTRICT,
    credential_id UUID NULL REFERENCES credentials(id) ON DELETE SET NULL,
    reason TEXT NOT NULL DEFAULT 'legacy_profile',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, legacy_owner_id),
    CHECK (char_length(reason) <= 128)
);

CREATE INDEX IF NOT EXISTS idx_ownership_aliases_canonical
    ON ownership_aliases(team_id, canonical_identity_id);

CREATE TABLE IF NOT EXISTS identity_compatibility_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    bridge_version TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('bridge_active', 'reconciled', 'cutover_ready', 'cleanup_complete')),
    legacy_profile_count BIGINT NOT NULL DEFAULT 0 CHECK (legacy_profile_count >= 0),
    identity_count BIGINT NOT NULL DEFAULT 0 CHECK (identity_count >= 0),
    membership_count BIGINT NOT NULL DEFAULT 0 CHECK (membership_count >= 0),
    credential_count BIGINT NOT NULL DEFAULT 0 CHECK (credential_count >= 0),
    alias_count BIGINT NOT NULL DEFAULT 0 CHECK (alias_count >= 0),
    unresolved_count BIGINT NOT NULL DEFAULT 0 CHECK (unresolved_count >= 0),
    backup_checkpoint TEXT NOT NULL DEFAULT '',
    deployment_fingerprint TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE actor_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE actor_identities FORCE ROW LEVEL SECURITY;
ALTER TABLE team_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE team_memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE membership_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE identity_external_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity_external_links FORCE ROW LEVEL SECURITY;
ALTER TABLE ownership_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE ownership_aliases FORCE ROW LEVEL SECURITY;
ALTER TABLE identity_compatibility_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity_compatibility_state FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS actor_identities_context_access ON actor_identities;
CREATE POLICY actor_identities_context_access ON actor_identities
    FOR ALL TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
    );

DROP POLICY IF EXISTS team_memberships_context_access ON team_memberships;
CREATE POLICY team_memberships_context_access ON team_memberships
    FOR ALL TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                              NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                              NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );

DROP POLICY IF EXISTS credentials_context_access ON credentials;
CREATE POLICY credentials_context_access ON credentials
    FOR ALL TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                              NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                              NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );

DROP POLICY IF EXISTS membership_grants_context_access ON membership_grants;
CREATE POLICY membership_grants_context_access ON membership_grants
    FOR ALL TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR EXISTS (
            SELECT 1 FROM team_memberships m
            WHERE m.id = membership_id
              AND m.team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                                       NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
        )
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR EXISTS (
            SELECT 1 FROM team_memberships m
            WHERE m.id = membership_id
              AND m.team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                                       NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
        )
    );

DROP POLICY IF EXISTS identity_external_links_context_access ON identity_external_links;
CREATE POLICY identity_external_links_context_access ON identity_external_links
    FOR ALL TO PUBLIC
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

DROP POLICY IF EXISTS ownership_aliases_context_access ON ownership_aliases;
CREATE POLICY ownership_aliases_context_access ON ownership_aliases
    FOR ALL TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                              NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = COALESCE(NULLIF(current_setting('app.current_team_id', true), '')::uuid,
                              NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );

DROP POLICY IF EXISTS identity_compatibility_state_context_access ON identity_compatibility_state;
CREATE POLICY identity_compatibility_state_context_access ON identity_compatibility_state
    FOR ALL TO PUBLIC
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));

-- Existing API-key profiles retain their IDs as both actor and credential IDs.
-- The statement is idempotent so a retry after a rolled-back deployment is safe.
INSERT INTO actor_identities (id, kind, team_id, display_name, active, created_at, updated_at)
SELECT p.id,
       CASE WHEN p.auth_source = 'sso' THEN 'human'
            WHEN p.auth_source = 'system' THEN 'system'
            ELSE 'api_client' END,
       p.team_id, COALESCE(p.name, ''), p.revoked_at IS NULL, p.created_at, p.updated_at
FROM team_profiles p
ON CONFLICT (id) DO UPDATE SET
    kind = EXCLUDED.kind,
    team_id = EXCLUDED.team_id,
    display_name = EXCLUDED.display_name,
    active = EXCLUDED.active,
    updated_at = EXCLUDED.updated_at;

INSERT INTO team_memberships (actor_identity_id, team_id, status, team_admin, maximum_grants, legacy_profile_id, created_at, updated_at)
SELECT p.id, p.team_id,
       CASE WHEN p.revoked_at IS NULL THEN 'active' ELSE 'revoked' END,
       p.role = 'manager', p.scopes, p.id, p.created_at, p.updated_at
FROM team_profiles p
ON CONFLICT (actor_identity_id, team_id) DO UPDATE SET
    status = EXCLUDED.status,
    team_admin = EXCLUDED.team_admin,
    maximum_grants = EXCLUDED.maximum_grants,
    legacy_profile_id = EXCLUDED.legacy_profile_id,
    updated_at = EXCLUDED.updated_at;

INSERT INTO credentials (id, actor_identity_id, owner_identity_id, team_id, kind, key_hash, key_prefix, key_suffix, name, scopes, rate_limit, status, expires_at, revoked_at, legacy_profile_id, created_at, updated_at, last_used_at)
SELECT p.id, p.id, NULL::uuid, p.team_id, 'api_key', p.key_hash, p.key_prefix, p.key_suffix, p.name, p.scopes,
       p.rate_limit,
       CASE WHEN p.revoked_at IS NOT NULL THEN 'revoked' WHEN p.expires_at IS NOT NULL AND p.expires_at <= now() THEN 'expired' ELSE 'active' END,
       p.expires_at, p.revoked_at, p.id, p.created_at, p.updated_at, p.last_used_at
FROM team_profiles p
WHERE p.key_hash IS NOT NULL AND p.key_prefix IS NOT NULL
ON CONFLICT (id) DO UPDATE SET
    key_hash = EXCLUDED.key_hash,
    key_prefix = EXCLUDED.key_prefix,
    key_suffix = EXCLUDED.key_suffix,
    name = EXCLUDED.name,
    scopes = EXCLUDED.scopes,
    rate_limit = EXCLUDED.rate_limit,
    status = EXCLUDED.status,
    expires_at = EXCLUDED.expires_at,
    revoked_at = EXCLUDED.revoked_at,
    owner_identity_id = EXCLUDED.owner_identity_id,
    updated_at = EXCLUDED.updated_at,
    last_used_at = EXCLUDED.last_used_at;

INSERT INTO membership_grants (membership_id, grant_name, source)
SELECT m.id, scope, 'legacy_scope'
FROM team_memberships m
JOIN LATERAL unnest(m.maximum_grants) AS scope ON true
ON CONFLICT (membership_id, grant_name) DO NOTHING;

INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id)
SELECT p.team_id, p.id, p.id,
       CASE WHEN p.key_hash IS NOT NULL AND p.key_prefix IS NOT NULL THEN p.id ELSE NULL END
FROM team_profiles p
ON CONFLICT (team_id, legacy_owner_id) DO UPDATE SET
    canonical_identity_id = EXCLUDED.canonical_identity_id,
    credential_id = EXCLUDED.credential_id;

-- SSO identities have stable external links. Existing profile IDs remain the
-- ownership aliases; the SSO actor is added only when it is not already linked.
INSERT INTO actor_identities (id, kind, provider, subject, display_name, active, created_at, updated_at)
SELECT i.id, 'human', p.id::text, i.subject, COALESCE(i.display_name, ''), i.active, i.created_at, i.updated_at
FROM sso_identities i
JOIN sso_providers p ON p.id = i.provider_id
ON CONFLICT (id) DO UPDATE SET
    kind = CASE WHEN actor_identities.kind = 'api_client' THEN actor_identities.kind ELSE EXCLUDED.kind END,
    provider = EXCLUDED.provider,
    subject = EXCLUDED.subject,
    display_name = EXCLUDED.display_name,
    active = EXCLUDED.active,
    updated_at = EXCLUDED.updated_at;

UPDATE credentials c
SET owner_identity_id = p.sso_owner_identity_id
FROM team_profiles p
WHERE c.legacy_profile_id = p.id
  AND p.sso_owner_identity_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM actor_identities a WHERE a.id = p.sso_owner_identity_id);

INSERT INTO identity_external_links (identity_id, provider, external_id)
SELECT i.id, p.id::text, COALESCE(NULLIF(i.external_id, ''), i.subject)
FROM sso_identities i
JOIN sso_providers p ON p.id = i.provider_id
WHERE COALESCE(NULLIF(i.external_id, ''), i.subject) <> ''
ON CONFLICT (provider, external_id) DO NOTHING;

-- SSO profiles keep their legacy IDs as aliases, but their stable SSO actors
-- own the canonical membership and its existing grant rows.
UPDATE team_memberships m
SET actor_identity_id = p.sso_identity_id,
    updated_at = now()
FROM team_profiles p
WHERE m.legacy_profile_id = p.id
  AND p.auth_source = 'sso'
  AND p.sso_identity_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM actor_identities a WHERE a.id = p.sso_identity_id);

UPDATE ownership_aliases a
SET canonical_identity_id = p.sso_identity_id,
    credential_id = NULL
FROM team_profiles p
WHERE a.team_id = p.team_id
  AND a.legacy_owner_id = p.id
  AND p.auth_source = 'sso'
  AND p.sso_identity_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM actor_identities i WHERE i.id = p.sso_identity_id);

INSERT INTO identity_compatibility_state (singleton, bridge_version, state, legacy_profile_count, identity_count, membership_count, credential_count, alias_count, unresolved_count, updated_at)
SELECT true, 'dense-mem.v2.5.identity.bridge.v1', 'bridge_active',
       (SELECT count(*) FROM team_profiles),
       (SELECT count(*) FROM actor_identities),
       (SELECT count(*) FROM team_memberships),
       (SELECT count(*) FROM credentials),
       (SELECT count(*) FROM ownership_aliases),
       (SELECT count(*) FROM team_profiles p WHERE NOT EXISTS (SELECT 1 FROM ownership_aliases a WHERE a.team_id = p.team_id AND a.legacy_owner_id = p.id)),
       now()
ON CONFLICT (singleton) DO UPDATE SET
    legacy_profile_count = EXCLUDED.legacy_profile_count,
    identity_count = EXCLUDED.identity_count,
    membership_count = EXCLUDED.membership_count,
    credential_count = EXCLUDED.credential_count,
    alias_count = EXCLUDED.alias_count,
    unresolved_count = EXCLUDED.unresolved_count,
    updated_at = EXCLUDED.updated_at;

CREATE OR REPLACE FUNCTION dense_mem_sync_legacy_profile_identity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    new_membership_id UUID;
    canonical_actor_id UUID;
BEGIN
    IF TG_OP = 'DELETE' THEN
        UPDATE credentials
           SET status = 'revoked', revoked_at = COALESCE(revoked_at, now()), updated_at = now()
         WHERE id = OLD.id;
        UPDATE team_memberships
           SET status = 'revoked', updated_at = now()
         WHERE legacy_profile_id = OLD.id
            OR (actor_identity_id = OLD.id AND team_id = OLD.team_id);
		UPDATE actor_identities
		   SET active = false, updated_at = now()
		 WHERE id = OLD.id;
		-- Keep the alias after a legacy-row delete so append-only owner references
		-- remain resolvable during and after the compatibility window.
		RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE'
       AND NEW.last_used_at IS DISTINCT FROM OLD.last_used_at
       AND NEW.team_id IS NOT DISTINCT FROM OLD.team_id
       AND NEW.key_hash IS NOT DISTINCT FROM OLD.key_hash
       AND NEW.key_prefix IS NOT DISTINCT FROM OLD.key_prefix
       AND NEW.key_suffix IS NOT DISTINCT FROM OLD.key_suffix
       AND NEW.name IS NOT DISTINCT FROM OLD.name
       AND NEW.scopes IS NOT DISTINCT FROM OLD.scopes
       AND NEW.role IS NOT DISTINCT FROM OLD.role
       AND NEW.rate_limit IS NOT DISTINCT FROM OLD.rate_limit
       AND NEW.expires_at IS NOT DISTINCT FROM OLD.expires_at
       AND NEW.revoked_at IS NOT DISTINCT FROM OLD.revoked_at
       AND NEW.auth_source IS NOT DISTINCT FROM OLD.auth_source
       AND NEW.is_system IS NOT DISTINCT FROM OLD.is_system
       AND NEW.sso_identity_id IS NOT DISTINCT FROM OLD.sso_identity_id
       AND NEW.sso_provider_id IS NOT DISTINCT FROM OLD.sso_provider_id
       AND NEW.sso_subject IS NOT DISTINCT FROM OLD.sso_subject
       AND NEW.sso_email IS NOT DISTINCT FROM OLD.sso_email
       AND NEW.sso_group_id IS NOT DISTINCT FROM OLD.sso_group_id
       AND NEW.sso_entitlement_status IS NOT DISTINCT FROM OLD.sso_entitlement_status
       AND NEW.sso_owner_identity_id IS NOT DISTINCT FROM OLD.sso_owner_identity_id
       AND NEW.created_at IS NOT DISTINCT FROM OLD.created_at
       AND NEW.updated_at IS NOT DISTINCT FROM OLD.updated_at
    THEN
        UPDATE credentials
           SET last_used_at = NEW.last_used_at
         WHERE id = NEW.id
           AND last_used_at IS DISTINCT FROM NEW.last_used_at;
        RETURN NEW;
    END IF;

    INSERT INTO actor_identities (id, kind, team_id, display_name, active, created_at, updated_at)
    VALUES (NEW.id,
            CASE WHEN NEW.auth_source = 'sso' THEN 'human'
                 WHEN NEW.auth_source = 'system' THEN 'system'
                 ELSE 'api_client' END,
            NEW.team_id, COALESCE(NEW.name, ''), NEW.revoked_at IS NULL, COALESCE(NEW.created_at, now()), now())
    ON CONFLICT (id) DO UPDATE SET team_id = EXCLUDED.team_id, display_name = EXCLUDED.display_name, active = EXCLUDED.active, updated_at = now();

    canonical_actor_id := NEW.id;
    IF NEW.auth_source = 'sso'
       AND NEW.sso_identity_id IS NOT NULL
       AND EXISTS (SELECT 1 FROM actor_identities a WHERE a.id = NEW.sso_identity_id)
    THEN
        canonical_actor_id := NEW.sso_identity_id;
    END IF;

    UPDATE team_memberships
       SET actor_identity_id = canonical_actor_id,
           team_id = NEW.team_id,
           status = CASE WHEN NEW.revoked_at IS NULL THEN 'active' ELSE 'revoked' END,
           team_admin = NEW.role = 'manager',
           maximum_grants = NEW.scopes,
           updated_at = now()
     WHERE legacy_profile_id = NEW.id
     RETURNING id INTO new_membership_id;

    IF NOT FOUND THEN
        INSERT INTO team_memberships (actor_identity_id, team_id, status, team_admin, maximum_grants, legacy_profile_id)
        VALUES (canonical_actor_id, NEW.team_id, CASE WHEN NEW.revoked_at IS NULL THEN 'active' ELSE 'revoked' END, NEW.role = 'manager', NEW.scopes, NEW.id)
        ON CONFLICT (actor_identity_id, team_id) DO UPDATE SET status = EXCLUDED.status, team_admin = EXCLUDED.team_admin, maximum_grants = EXCLUDED.maximum_grants, legacy_profile_id = EXCLUDED.legacy_profile_id, updated_at = now()
        RETURNING id INTO new_membership_id;
    END IF;

    IF NEW.key_hash IS NOT NULL AND NEW.key_prefix IS NOT NULL THEN
        INSERT INTO credentials (id, actor_identity_id, owner_identity_id, team_id, kind, key_hash, key_prefix, key_suffix, name, scopes, rate_limit, status, expires_at, revoked_at, legacy_profile_id, created_at, updated_at, last_used_at)
        VALUES (NEW.id, NEW.id, CASE WHEN EXISTS (SELECT 1 FROM actor_identities a WHERE a.id = NEW.sso_owner_identity_id) THEN NEW.sso_owner_identity_id ELSE NULL END, NEW.team_id, 'api_key', NEW.key_hash, NEW.key_prefix, NEW.key_suffix, NEW.name, NEW.scopes, NEW.rate_limit,
                CASE WHEN NEW.revoked_at IS NOT NULL THEN 'revoked' WHEN NEW.expires_at IS NOT NULL AND NEW.expires_at <= now() THEN 'expired' ELSE 'active' END,
                NEW.expires_at, NEW.revoked_at, NEW.id, COALESCE(NEW.created_at, now()), now(), NEW.last_used_at)
        ON CONFLICT (id) DO UPDATE SET key_hash = EXCLUDED.key_hash, key_prefix = EXCLUDED.key_prefix, key_suffix = EXCLUDED.key_suffix, name = EXCLUDED.name, scopes = EXCLUDED.scopes, rate_limit = EXCLUDED.rate_limit, status = EXCLUDED.status, expires_at = EXCLUDED.expires_at, revoked_at = EXCLUDED.revoked_at, owner_identity_id = EXCLUDED.owner_identity_id, updated_at = now(), last_used_at = EXCLUDED.last_used_at;
    END IF;

    INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id)
    VALUES (NEW.team_id, NEW.id, canonical_actor_id,
            CASE WHEN NEW.key_hash IS NOT NULL AND NEW.key_prefix IS NOT NULL THEN NEW.id ELSE NULL END)
    ON CONFLICT (team_id, legacy_owner_id) DO UPDATE SET canonical_identity_id = EXCLUDED.canonical_identity_id, credential_id = EXCLUDED.credential_id;

	DELETE FROM membership_grants
	 WHERE membership_grants.membership_id = new_membership_id
	   AND membership_grants.source = 'legacy_scope'
	   AND NOT (membership_grants.grant_name = ANY(COALESCE(NEW.scopes, ARRAY[]::text[])));
    INSERT INTO membership_grants (membership_id, grant_name, source)
    SELECT new_membership_id, scope, 'legacy_scope' FROM unnest(NEW.scopes) AS scope
    ON CONFLICT (membership_id, grant_name) DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS team_profiles_identity_bridge ON team_profiles;
CREATE TRIGGER team_profiles_identity_bridge
AFTER INSERT OR UPDATE OF team_id, key_hash, key_prefix, key_suffix, name, scopes, role, rate_limit, expires_at, revoked_at, last_used_at, auth_source, is_system, sso_identity_id, sso_provider_id, sso_subject, sso_email, sso_group_id, sso_entitlement_status, sso_owner_identity_id OR DELETE
ON team_profiles
FOR EACH ROW EXECUTE FUNCTION dense_mem_sync_legacy_profile_identity();

CREATE OR REPLACE FUNCTION dense_mem_sync_sso_identity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    provider_key TEXT;
    external_key TEXT;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM identity_external_links
        WHERE identity_id = OLD.id;

        UPDATE actor_identities
        SET active = false,
            updated_at = now()
        WHERE id = OLD.id;
        RETURN OLD;
    END IF;

    provider_key := NEW.provider_id::text;
    external_key := COALESCE(NULLIF(NEW.external_id, ''), NULLIF(NEW.subject, ''), '');

    INSERT INTO actor_identities (id, kind, provider, subject, display_name, active, created_at, updated_at)
    VALUES (NEW.id, 'human', provider_key, NEW.subject, COALESCE(NEW.display_name, ''), NEW.active, COALESCE(NEW.created_at, now()), now())
    ON CONFLICT (id) DO UPDATE SET
        kind = CASE WHEN actor_identities.kind = 'api_client' THEN actor_identities.kind ELSE EXCLUDED.kind END,
        provider = EXCLUDED.provider,
        subject = EXCLUDED.subject,
        display_name = EXCLUDED.display_name,
        active = EXCLUDED.active,
        updated_at = now();

    IF TG_OP = 'UPDATE' THEN
        DELETE FROM identity_external_links
        WHERE identity_id = OLD.id
          AND (provider <> provider_key OR external_id <> external_key);
    END IF;

    IF external_key <> '' THEN
        INSERT INTO identity_external_links (identity_id, provider, external_id)
        VALUES (NEW.id, provider_key, external_key)
        ON CONFLICT (provider, external_id) DO UPDATE
        SET identity_id = EXCLUDED.identity_id;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS sso_identities_identity_bridge ON sso_identities;
CREATE TRIGGER sso_identities_identity_bridge
AFTER INSERT OR UPDATE OF provider_id, subject, external_id, display_name, active OR DELETE
ON sso_identities
FOR EACH ROW EXECUTE FUNCTION dense_mem_sync_sso_identity();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS team_profiles_identity_bridge ON team_profiles;
DROP FUNCTION IF EXISTS dense_mem_sync_legacy_profile_identity();
DROP TRIGGER IF EXISTS sso_identities_identity_bridge ON sso_identities;
DROP FUNCTION IF EXISTS dense_mem_sync_sso_identity();
DROP TABLE IF EXISTS identity_compatibility_state;
DROP TABLE IF EXISTS ownership_aliases;
DROP TABLE IF EXISTS identity_external_links;
DROP TABLE IF EXISTS membership_grants;
DROP TABLE IF EXISTS credentials;
DROP TABLE IF EXISTS team_memberships;
DROP TABLE IF EXISTS actor_identities;

-- +goose StatementEnd
