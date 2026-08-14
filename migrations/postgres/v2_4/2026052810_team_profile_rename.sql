-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF to_regclass('public.profiles') IS NOT NULL THEN
        ALTER TABLE profiles DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.teams') IS NOT NULL THEN
        ALTER TABLE teams DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.audit_log') IS NOT NULL THEN
        ALTER TABLE audit_log DISABLE ROW LEVEL SECURITY;
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.teams') IS NULL AND to_regclass('public.profiles') IS NOT NULL THEN
        ALTER TABLE profiles RENAME TO teams;
    END IF;

    IF to_regclass('public.team_profiles') IS NULL AND to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys RENAME TO team_profiles;
    END IF;

    IF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_suffix VARCHAR(6) NULL;
        ALTER TABLE api_keys ALTER COLUMN key_prefix TYPE VARCHAR(24);
    END IF;

    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles ADD COLUMN IF NOT EXISTS key_suffix VARCHAR(6) NULL;
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.profiles') IS NOT NULL AND to_regclass('public.teams') IS NOT NULL THEN
        INSERT INTO teams (
            id,
            name,
            description,
            metadata,
            config,
            status,
            created_at,
            updated_at,
            deleted_at
        )
        SELECT
            id,
            name,
            description,
            metadata,
            config,
            status,
            created_at,
            updated_at,
            deleted_at
        FROM profiles
        ON CONFLICT (id) DO UPDATE SET
            name = EXCLUDED.name,
            description = EXCLUDED.description,
            metadata = EXCLUDED.metadata,
            config = EXCLUDED.config,
            status = EXCLUDED.status,
            created_at = EXCLUDED.created_at,
            updated_at = EXCLUDED.updated_at,
            deleted_at = EXCLUDED.deleted_at;
    END IF;
END $$;

DO $$
BEGIN
    IF to_regclass('public.api_keys') IS NOT NULL AND to_regclass('public.team_profiles') IS NOT NULL THEN
        WITH normalized AS (
            SELECT
                id,
                profile_id,
                key_hash,
                key_prefix,
                key_suffix,
                COALESCE(NULLIF(trim(label), ''), 'default profile') AS base_name,
                scopes,
                rate_limit,
                expires_at,
                revoked_at,
                last_used_at,
                created_at,
                updated_at
            FROM api_keys
        ),
        ranked AS (
            SELECT
                *,
                row_number() OVER (
                    PARTITION BY profile_id, lower(base_name)
                    ORDER BY created_at ASC, id ASC
                ) AS name_rank
            FROM normalized
        )
        INSERT INTO team_profiles (
            id,
            team_id,
            key_hash,
            key_prefix,
            key_suffix,
            name,
            scopes,
            rate_limit,
            expires_at,
            revoked_at,
            last_used_at,
            created_at,
            updated_at
        )
        SELECT
            id,
            profile_id,
            key_hash,
            key_prefix,
            key_suffix,
            CASE
                WHEN name_rank = 1 THEN left(base_name, 100)
                ELSE left(base_name, GREATEST(1, 100 - length(' ' || name_rank::text))) || ' ' || name_rank::text
            END,
            scopes,
            rate_limit,
            expires_at,
            revoked_at,
            last_used_at,
            created_at,
            updated_at
        FROM ranked
        ON CONFLICT (id) DO UPDATE SET
            team_id = EXCLUDED.team_id,
            key_hash = EXCLUDED.key_hash,
            key_prefix = EXCLUDED.key_prefix,
            key_suffix = EXCLUDED.key_suffix,
            name = EXCLUDED.name,
            scopes = EXCLUDED.scopes,
            rate_limit = EXCLUDED.rate_limit,
            expires_at = EXCLUDED.expires_at,
            revoked_at = EXCLUDED.revoked_at,
            last_used_at = EXCLUDED.last_used_at,
            created_at = EXCLUDED.created_at,
            updated_at = EXCLUDED.updated_at;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'team_profiles' AND column_name = 'profile_id'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'team_profiles' AND column_name = 'team_id'
        ) THEN
            UPDATE team_profiles SET team_id = profile_id WHERE team_id IS NULL;
        ELSE
            ALTER TABLE team_profiles RENAME COLUMN profile_id TO team_id;
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'team_profiles' AND column_name = 'label'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'team_profiles' AND column_name = 'name'
        ) THEN
            UPDATE team_profiles
            SET name = COALESCE(NULLIF(trim(label), ''), 'default profile')
            WHERE name IS NULL OR name = '';
        ELSE
            ALTER TABLE team_profiles RENAME COLUMN label TO name;
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'profile_id'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'team_id'
        ) THEN
            UPDATE audit_log SET team_id = profile_id WHERE team_id IS NULL;
        ELSE
            ALTER TABLE audit_log RENAME COLUMN profile_id TO team_id;
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'actor_key_id'
    ) THEN
        IF EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'actor_profile_id'
        ) THEN
            UPDATE audit_log SET actor_profile_id = actor_key_id WHERE actor_profile_id IS NULL;
        ELSE
            ALTER TABLE audit_log RENAME COLUMN actor_key_id TO actor_profile_id;
        END IF;
    END IF;
END $$;

ALTER TABLE team_profiles
    ALTER COLUMN key_prefix TYPE VARCHAR(24);

DO $$
BEGIN
    IF to_regclass('public.idx_profiles_name_unique_active') IS NOT NULL
        AND to_regclass('public.idx_teams_name_unique_active') IS NULL THEN
        ALTER INDEX idx_profiles_name_unique_active RENAME TO idx_teams_name_unique_active;
    END IF;

    IF to_regclass('public.idx_api_keys_profile_id') IS NOT NULL
        AND to_regclass('public.idx_team_profiles_team_id') IS NULL THEN
        ALTER INDEX idx_api_keys_profile_id RENAME TO idx_team_profiles_team_id;
    END IF;

    IF to_regclass('public.idx_api_keys_key_prefix_unique') IS NOT NULL
        AND to_regclass('public.idx_team_profiles_key_prefix_unique') IS NULL THEN
        ALTER INDEX idx_api_keys_key_prefix_unique RENAME TO idx_team_profiles_key_prefix_unique;
    END IF;

    IF to_regclass('public.idx_audit_log_profile_timestamp') IS NOT NULL
        AND to_regclass('public.idx_audit_log_team_timestamp') IS NULL THEN
        ALTER INDEX idx_audit_log_profile_timestamp RENAME TO idx_audit_log_team_timestamp;
    END IF;
END $$;

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

WITH ranked AS (
    SELECT
        id,
        COALESCE(NULLIF(trim(name), ''), 'default profile') AS base_name,
        row_number() OVER (
            PARTITION BY team_id, lower(COALESCE(NULLIF(trim(name), ''), 'default profile'))
            ORDER BY created_at ASC, id ASC
        ) AS name_rank
    FROM team_profiles
)
UPDATE team_profiles tp
SET name = CASE
    WHEN ranked.name_rank = 1 THEN left(ranked.base_name, 100)
    ELSE left(ranked.base_name, GREATEST(1, 100 - length(' ' || ranked.name_rank::text))) || ' ' || ranked.name_rank::text
END
FROM ranked
WHERE tp.id = ranked.id;

ALTER TABLE team_profiles ALTER COLUMN name SET NOT NULL;
ALTER TABLE team_profiles ALTER COLUMN name SET DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_teams_name_unique_active
    ON teams (lower(name))
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_team_profiles_team_id ON team_profiles(team_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_key_prefix_unique ON team_profiles(key_prefix);
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_team_name_unique ON team_profiles(team_id, lower(name));
CREATE INDEX IF NOT EXISTS idx_audit_log_team_timestamp ON audit_log(team_id, timestamp DESC);

DO $$
BEGIN
    ALTER TABLE teams ENABLE ROW LEVEL SECURITY;
    ALTER TABLE teams FORCE ROW LEVEL SECURITY;
    ALTER TABLE team_profiles ENABLE ROW LEVEL SECURITY;
    ALTER TABLE team_profiles FORCE ROW LEVEL SECURITY;
    ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
    ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;

    IF to_regclass('public.profiles') IS NOT NULL THEN
        ALTER TABLE profiles ENABLE ROW LEVEL SECURITY;
        ALTER TABLE profiles FORCE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
        ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;
    END IF;

    DROP POLICY IF EXISTS profiles_self_access ON teams;
    DROP POLICY IF EXISTS profiles_system_read_access ON teams;
    DROP POLICY IF EXISTS teams_self_access ON teams;
    DROP POLICY IF EXISTS teams_system_read_access ON teams;

    DROP POLICY IF EXISTS api_keys_self_access ON team_profiles;
    DROP POLICY IF EXISTS api_keys_system_read_access ON team_profiles;
    DROP POLICY IF EXISTS api_keys_system_update_access ON team_profiles;
    DROP POLICY IF EXISTS team_profiles_self_access ON team_profiles;
    DROP POLICY IF EXISTS team_profiles_system_read_access ON team_profiles;
    DROP POLICY IF EXISTS team_profiles_system_update_access ON team_profiles;

    DROP POLICY IF EXISTS audit_log_insert_all ON audit_log;
    DROP POLICY IF EXISTS audit_log_system_read_access ON audit_log;
    DROP POLICY IF EXISTS audit_log_self_access ON audit_log;

    CREATE POLICY teams_self_access ON teams
        FOR ALL
        TO PUBLIC
        USING (
            id = COALESCE(
                nullif(current_setting('app.current_team_id', true), '')::uuid,
                nullif(current_setting('app.current_profile_id', true), '')::uuid
            )
        )
        WITH CHECK (
            id = COALESCE(
                nullif(current_setting('app.current_team_id', true), '')::uuid,
                nullif(current_setting('app.current_profile_id', true), '')::uuid
            )
        );

    CREATE POLICY teams_system_read_access ON teams
        FOR SELECT
        TO PUBLIC
        USING (
            current_setting('app.tx_mode', true) = 'system'
        );

    CREATE POLICY team_profiles_self_access ON team_profiles
        FOR ALL
        TO PUBLIC
        USING (
            team_id = COALESCE(
                nullif(current_setting('app.current_team_id', true), '')::uuid,
                nullif(current_setting('app.current_profile_id', true), '')::uuid
            )
        )
        WITH CHECK (
            team_id = COALESCE(
                nullif(current_setting('app.current_team_id', true), '')::uuid,
                nullif(current_setting('app.current_profile_id', true), '')::uuid
            )
        );

    CREATE POLICY team_profiles_system_read_access ON team_profiles
        FOR SELECT
        TO PUBLIC
        USING (
            current_setting('app.tx_mode', true) = 'system'
        );

    CREATE POLICY team_profiles_system_update_access ON team_profiles
        FOR UPDATE
        TO PUBLIC
        USING (
            current_setting('app.tx_mode', true) = 'system'
        )
        WITH CHECK (
            current_setting('app.tx_mode', true) = 'system'
        );

    CREATE POLICY audit_log_self_access ON audit_log
        FOR SELECT
        TO PUBLIC
        USING (
            team_id = COALESCE(
                nullif(current_setting('app.current_team_id', true), '')::uuid,
                nullif(current_setting('app.current_profile_id', true), '')::uuid
            )
        );

    CREATE POLICY audit_log_system_read_access ON audit_log
        FOR SELECT
        TO PUBLIC
        USING (
            current_setting('app.tx_mode', true) = 'system'
        );

    CREATE POLICY audit_log_insert_all ON audit_log
        FOR INSERT
        TO PUBLIC
        WITH CHECK (
            current_setting('app.tx_mode', true) IN ('team', 'profile', 'system')
            AND (
                current_setting('app.tx_mode', true) = 'system'
                OR team_id IS NULL
                OR team_id = COALESCE(
                    nullif(current_setting('app.current_team_id', true), '')::uuid,
                    nullif(current_setting('app.current_profile_id', true), '')::uuid
                )
            )
        );
END $$;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_audit_log_team_timestamp;
DROP INDEX IF EXISTS idx_team_profiles_team_name_unique;
DROP INDEX IF EXISTS idx_team_profiles_key_prefix_unique;
DROP INDEX IF EXISTS idx_team_profiles_team_id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'actor_profile_id'
    ) THEN
        ALTER TABLE audit_log RENAME COLUMN actor_profile_id TO actor_key_id;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'team_id'
    ) THEN
        ALTER TABLE audit_log RENAME COLUMN team_id TO profile_id;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'team_profiles' AND column_name = 'name'
    ) THEN
        ALTER TABLE team_profiles RENAME COLUMN name TO label;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'team_profiles' AND column_name = 'team_id'
    ) THEN
        ALTER TABLE team_profiles RENAME COLUMN team_id TO profile_id;
    END IF;

    IF to_regclass('public.api_keys') IS NULL AND to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles RENAME TO api_keys;
    END IF;

    IF to_regclass('public.profiles') IS NULL AND to_regclass('public.teams') IS NOT NULL THEN
        ALTER TABLE teams RENAME TO profiles;
    END IF;
END $$;

-- +goose StatementEnd
