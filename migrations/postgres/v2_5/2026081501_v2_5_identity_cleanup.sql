-- +goose Up
-- +goose StatementBegin

-- This is the irreversible contract phase for the v2.5 identity bridge.
-- Lock/rewrite impact: team_profiles is locked for at most 30 seconds; retained
-- tables receive metadata-only columns and constraint/index updates.
-- RLS impact: canonical identity policies no longer depend on the legacy table.
-- Backfill: SSO membership metadata, profile names, and session membership IDs are translated
-- while the legacy table is locked, then every retained reference is checked.
-- Backward compatibility: existing IDs and keys survive; old binaries are not
-- supported after the destructive transaction commits.
-- Rollback: any mismatch or lock failure rolls back this whole migration.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('lock_timeout', '30s', true);

LOCK TABLE team_profiles IN ACCESS EXCLUSIVE MODE;

-- Older FORCE RLS policies predate migration mode, so expose legacy rows only
-- for this cleanup transaction before rerunning both bridge paths.
CREATE POLICY team_profiles_identity_cleanup_migration_access ON team_profiles
    FOR ALL TO PUBLIC
    USING (current_setting('app.tx_mode', true) = 'migration')
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration');
CREATE POLICY sso_identities_identity_cleanup_migration_access ON sso_identities
    FOR ALL TO PUBLIC
    USING (current_setting('app.tx_mode', true) = 'migration')
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration');
CREATE POLICY sso_sessions_identity_cleanup_migration_access ON sso_sessions
    FOR ALL TO PUBLIC
    USING (current_setting('app.tx_mode', true) = 'migration')
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration');
CREATE POLICY usage_metric_buckets_identity_cleanup_migration_access ON usage_metric_buckets
    FOR ALL TO PUBLIC
    USING (current_setting('app.tx_mode', true) = 'migration')
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration');
CREATE POLICY user_portal_sessions_identity_cleanup_migration_access ON user_portal_sessions
    FOR ALL TO PUBLIC
    USING (current_setting('app.tx_mode', true) = 'migration')
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration');

-- Successful writes that committed before this lock are included in the final checks.
UPDATE sso_identities SET active = active;
UPDATE team_profiles SET last_used_at = last_used_at;

ALTER TABLE team_memberships
    ADD COLUMN IF NOT EXISTS sso_provider_id UUID NULL REFERENCES sso_providers(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS sso_group_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sso_profile_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sso_entitlement_status TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sso_last_entitlement_checked_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS sso_last_login_at TIMESTAMPTZ NULL;

ALTER TABLE team_memberships
    DROP CONSTRAINT IF EXISTS team_memberships_sso_group_id_check,
    DROP CONSTRAINT IF EXISTS team_memberships_sso_entitlement_status_check,
    ADD CONSTRAINT team_memberships_sso_group_id_check
        CHECK (char_length(sso_group_id) <= 512),
    ADD CONSTRAINT team_memberships_sso_entitlement_status_check
        CHECK (sso_entitlement_status IN ('', 'active', 'denied', 'error'));

UPDATE team_memberships AS membership
SET sso_provider_id = profile.sso_provider_id,
    sso_group_id = COALESCE(profile.sso_group_id, ''),
    sso_profile_name = COALESCE(profile.name, ''),
    sso_entitlement_status = profile.sso_entitlement_status,
    sso_last_entitlement_checked_at = profile.sso_last_entitlement_checked_at,
    sso_last_login_at = profile.sso_last_login_at
FROM team_profiles AS profile
WHERE membership.legacy_profile_id = profile.id
  AND profile.auth_source = 'sso';

-- A deleted legacy row represents an intentional canonical tombstone. The
-- permanent alias stays available for append-only ownership history.
UPDATE credentials AS credential
SET status = 'disabled',
    revoked_at = COALESCE(credential.revoked_at, now()),
    updated_at = now()
WHERE (
    (
        credential.legacy_profile_id IS NOT NULL
        AND NOT EXISTS (
            SELECT 1
            FROM team_profiles AS profile
            WHERE profile.id = credential.legacy_profile_id
        )
    )
    OR EXISTS (
        SELECT 1
        FROM ownership_aliases AS alias
        WHERE alias.team_id = credential.team_id
          AND alias.credential_id = credential.id
          AND NOT EXISTS (
              SELECT 1
              FROM team_profiles AS profile
              WHERE profile.team_id = alias.team_id
                AND profile.id = alias.legacy_owner_id
          )
    )
);

UPDATE team_memberships AS membership
SET status = 'revoked',
    updated_at = now()
WHERE membership.legacy_profile_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM team_profiles AS profile
      WHERE profile.id = membership.legacy_profile_id
  );

UPDATE actor_identities AS identity
SET active = false,
    updated_at = now()
WHERE identity.team_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM team_memberships AS membership
      WHERE membership.actor_identity_id = identity.id
        AND membership.status = 'active'
  );

DO $dense_mem_identity_cleanup_preflight$
DECLARE
    mismatch_count BIGINT;
    unexpected_dependencies TEXT;
BEGIN
    SELECT count(*)
    INTO mismatch_count
    FROM team_profiles AS profile
    LEFT JOIN team_memberships AS membership
      ON membership.legacy_profile_id = profile.id
    LEFT JOIN ownership_aliases AS alias
      ON alias.team_id = profile.team_id
     AND alias.legacy_owner_id = profile.id
    WHERE membership.id IS NULL
       OR membership.team_id <> profile.team_id
       OR membership.actor_identity_id <> CASE
              WHEN profile.auth_source = 'sso' THEN profile.sso_identity_id
              ELSE profile.id
          END
       OR membership.status <> CASE WHEN profile.revoked_at IS NULL THEN 'active' ELSE 'revoked' END
       OR membership.team_admin <> (profile.role = 'manager')
       OR membership.maximum_grants IS DISTINCT FROM profile.scopes
       OR alias.legacy_owner_id IS NULL
       OR alias.canonical_identity_id <> membership.actor_identity_id;
    IF mismatch_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: membership or ownership translation mismatch (% rows)', mismatch_count;
    END IF;

    SELECT count(*)
    INTO mismatch_count
    FROM team_profiles AS profile
    LEFT JOIN credentials AS credential ON credential.id = profile.id
    WHERE profile.auth_source = 'api_key'
      AND (
          credential.id IS NULL
          OR credential.actor_identity_id <> profile.id
          OR credential.team_id <> profile.team_id
          OR credential.kind <> 'api_key'
          OR credential.key_hash IS DISTINCT FROM profile.key_hash
          OR credential.key_prefix IS DISTINCT FROM profile.key_prefix
          OR credential.key_suffix IS DISTINCT FROM profile.key_suffix
          OR credential.name IS DISTINCT FROM profile.name
          OR credential.scopes IS DISTINCT FROM profile.scopes
          OR credential.rate_limit <> profile.rate_limit
          OR credential.expires_at IS DISTINCT FROM profile.expires_at
          OR credential.revoked_at IS DISTINCT FROM profile.revoked_at
          OR credential.owner_identity_id IS DISTINCT FROM profile.sso_owner_identity_id
      );
    IF mismatch_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: credential translation mismatch (% rows)', mismatch_count;
    END IF;

    SELECT count(*)
    INTO mismatch_count
    FROM team_profiles AS profile
    JOIN team_memberships AS membership ON membership.legacy_profile_id = profile.id
    WHERE EXISTS (
        SELECT 1
        FROM unnest(profile.scopes) AS expected(grant_name)
        WHERE NOT EXISTS (
            SELECT 1
            FROM membership_grants AS existing_grant
            WHERE existing_grant.membership_id = membership.id
              AND existing_grant.source = 'legacy_scope'
              AND existing_grant.grant_name = expected.grant_name
        )
    ) OR EXISTS (
        SELECT 1
        FROM membership_grants AS existing_grant
        WHERE existing_grant.membership_id = membership.id
          AND existing_grant.source = 'legacy_scope'
          AND NOT (existing_grant.grant_name = ANY(profile.scopes))
    );
    IF mismatch_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: membership grant translation mismatch (% rows)', mismatch_count;
    END IF;

    SELECT count(*)
    INTO mismatch_count
    FROM sso_identities AS identity
    JOIN sso_providers AS provider ON provider.id = identity.provider_id
    LEFT JOIN actor_identities AS actor ON actor.id = identity.id
    WHERE actor.id IS NULL
       OR actor.kind <> 'human'
       OR actor.team_id IS NOT NULL
       OR actor.provider <> provider.id::text
       OR actor.subject <> identity.subject
       OR actor.active <> identity.active;
    IF mismatch_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: SSO identity translation mismatch (% rows)', mismatch_count;
    END IF;

    SELECT string_agg(conrelid::regclass::text || '.' || conname, ', ' ORDER BY conrelid::regclass::text, conname)
    INTO unexpected_dependencies
    FROM pg_constraint
    WHERE confrelid = 'team_profiles'::regclass
      AND conname NOT IN (
          'credentials_legacy_profile_id_fkey',
          'semantic_profile_refs_team_id_profile_id_fkey',
          'sso_sessions_team_profile_id_fkey',
          'team_memberships_legacy_profile_id_fkey',
          'usage_metric_buckets_key_id_fkey',
          'user_portal_sessions_key_id_fkey',
          'v2_migration_corpus_items_team_id_owner_profile_id_fkey'
      );
    IF unexpected_dependencies IS NOT NULL THEN
        RAISE EXCEPTION 'identity cleanup blocked: unexpected team_profiles dependencies: %', unexpected_dependencies;
    END IF;
END;
$dense_mem_identity_cleanup_preflight$;

ALTER TABLE sso_sessions ADD COLUMN IF NOT EXISTS membership_id UUID NULL;

UPDATE sso_sessions AS session
SET membership_id = membership.id
FROM team_memberships AS membership
WHERE membership.legacy_profile_id = session.team_profile_id;

DO $dense_mem_identity_cleanup_refs$
DECLARE
    missing_count BIGINT;
BEGIN
    SELECT count(*) INTO missing_count FROM sso_sessions WHERE membership_id IS NULL;
    IF missing_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: SSO sessions missing memberships (% rows)', missing_count;
    END IF;

    SELECT count(*) INTO missing_count
    FROM semantic_profile_refs AS ref
    LEFT JOIN ownership_aliases AS alias
      ON alias.team_id = ref.team_id AND alias.legacy_owner_id = ref.profile_id
    WHERE alias.legacy_owner_id IS NULL;
    IF missing_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: semantic owners missing aliases (% rows)', missing_count;
    END IF;

    SELECT count(*) INTO missing_count
    FROM usage_metric_buckets AS bucket
    LEFT JOIN ownership_aliases AS alias
      ON alias.team_id = bucket.team_id AND alias.legacy_owner_id = bucket.key_id
    WHERE alias.legacy_owner_id IS NULL;
    IF missing_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: usage history missing ownership aliases (% rows)', missing_count;
    END IF;

    SELECT count(*) INTO missing_count
    FROM user_portal_sessions AS session
    LEFT JOIN credentials AS credential ON credential.id = session.key_id
    WHERE credential.id IS NULL;
    IF missing_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: portal sessions missing credentials (% rows)', missing_count;
    END IF;

    SELECT count(*) INTO missing_count
    FROM v2_migration_corpus_items AS item
    LEFT JOIN ownership_aliases AS alias
      ON alias.team_id = item.team_id AND alias.legacy_owner_id = item.owner_profile_id
    WHERE item.owner_profile_id IS NOT NULL AND alias.legacy_owner_id IS NULL;
    IF missing_count > 0 THEN
        RAISE EXCEPTION 'identity cleanup blocked: migration history missing ownership aliases (% rows)', missing_count;
    END IF;
END;
$dense_mem_identity_cleanup_refs$;

ALTER TABLE semantic_profile_refs
    DROP CONSTRAINT semantic_profile_refs_team_id_profile_id_fkey,
    ADD CONSTRAINT semantic_profile_refs_team_id_profile_id_fkey
        FOREIGN KEY (team_id, profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE CASCADE;

ALTER TABLE usage_metric_buckets
    DROP CONSTRAINT usage_metric_buckets_key_id_fkey,
    ADD CONSTRAINT usage_metric_buckets_key_id_fkey
        FOREIGN KEY (team_id, key_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE CASCADE;

ALTER TABLE user_portal_sessions
    DROP CONSTRAINT user_portal_sessions_key_id_fkey,
    ADD CONSTRAINT user_portal_sessions_key_id_fkey
        FOREIGN KEY (key_id) REFERENCES credentials(id) ON DELETE CASCADE;

ALTER TABLE v2_migration_corpus_items
    DROP CONSTRAINT v2_migration_corpus_items_team_id_owner_profile_id_fkey,
    ADD CONSTRAINT v2_migration_corpus_items_team_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE RESTRICT;

ALTER TABLE sso_sessions
    ALTER COLUMN membership_id SET NOT NULL,
    ADD CONSTRAINT sso_sessions_membership_id_fkey
        FOREIGN KEY (membership_id) REFERENCES team_memberships(id) ON DELETE CASCADE,
    DROP CONSTRAINT sso_sessions_team_profile_id_fkey,
    DROP COLUMN team_profile_id;

DROP POLICY actor_identities_context_access ON actor_identities;
CREATE POLICY actor_identities_context_access ON actor_identities
    FOR ALL TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        OR EXISTS (
            SELECT 1
            FROM team_memberships AS membership
            WHERE membership.actor_identity_id = actor_identities.id
              AND membership.team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        )
    )
    WITH CHECK (
		current_setting('app.tx_mode', true) = 'migration'
		OR (
			current_setting('app.tx_mode', true) = 'system'
			AND (
				kind <> 'system'
				OR team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
			)
		)
        OR team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        OR EXISTS (
            SELECT 1
            FROM team_memberships AS membership
            WHERE membership.actor_identity_id = actor_identities.id
              AND membership.team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY identity_external_links_context_access ON identity_external_links;
CREATE POLICY identity_external_links_context_access ON identity_external_links
    FOR ALL TO PUBLIC
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR EXISTS (
            SELECT 1
            FROM team_memberships AS membership
            WHERE membership.actor_identity_id = identity_external_links.identity_id
              AND membership.team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        )
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR EXISTS (
            SELECT 1
            FROM team_memberships AS membership
            WHERE membership.actor_identity_id = identity_external_links.identity_id
              AND membership.team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        )
    );

CREATE INDEX IF NOT EXISTS idx_team_memberships_sso_provider
    ON team_memberships(sso_provider_id, team_id, status)
    WHERE sso_provider_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_team_name_unique
    ON credentials(team_id, lower(name))
    WHERE kind = 'api_key' AND status <> 'disabled';
CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_owner_team_active_unique
    ON credentials(owner_identity_id, team_id)
    WHERE owner_identity_id IS NOT NULL AND kind = 'api_key' AND status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS actor_identities_system_team_unique
    ON actor_identities(team_id)
    WHERE kind = 'system';

DROP POLICY sso_identities_identity_cleanup_migration_access ON sso_identities;
DROP POLICY sso_sessions_identity_cleanup_migration_access ON sso_sessions;
DROP POLICY usage_metric_buckets_identity_cleanup_migration_access ON usage_metric_buckets;
DROP POLICY user_portal_sessions_identity_cleanup_migration_access ON user_portal_sessions;
DROP TRIGGER team_profiles_identity_bridge ON team_profiles;
DROP FUNCTION dense_mem_sync_legacy_profile_identity();
DROP TRIGGER sso_identities_identity_bridge ON sso_identities;
DROP FUNCTION dense_mem_sync_sso_identity();

ALTER TABLE credentials DROP COLUMN legacy_profile_id;
ALTER TABLE team_memberships DROP COLUMN legacy_profile_id;

DROP TABLE identity_compatibility_state;
DROP TABLE team_profiles;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $dense_mem_irreversible_identity_cleanup$
BEGIN
    RAISE EXCEPTION 'irreversible migration: v2.5 identity cleanup removed team_profiles and its compatibility bridge';
END;
$dense_mem_irreversible_identity_cleanup$;

-- +goose StatementEnd
