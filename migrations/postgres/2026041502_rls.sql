-- +goose Up
-- +goose StatementBegin

DO $$
BEGIN
    IF to_regclass('public.teams') IS NOT NULL THEN
        ALTER TABLE teams ENABLE ROW LEVEL SECURITY;
        ALTER TABLE teams FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS teams_self_access ON teams;
        DROP POLICY IF EXISTS teams_system_read_access ON teams;

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
    END IF;

    IF to_regclass('public.profiles') IS NOT NULL THEN
        ALTER TABLE profiles ENABLE ROW LEVEL SECURITY;
        ALTER TABLE profiles FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS profiles_self_access ON profiles;
        DROP POLICY IF EXISTS profiles_system_read_access ON profiles;

        CREATE POLICY profiles_self_access ON profiles
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

        CREATE POLICY profiles_system_read_access ON profiles
            FOR SELECT
            TO PUBLIC
            USING (
                current_setting('app.tx_mode', true) = 'system'
            );
    END IF;

    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles ENABLE ROW LEVEL SECURITY;
        ALTER TABLE team_profiles FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS team_profiles_self_access ON team_profiles;
        DROP POLICY IF EXISTS team_profiles_system_read_access ON team_profiles;
        DROP POLICY IF EXISTS team_profiles_system_update_access ON team_profiles;

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
    END IF;

    IF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
        ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;

        DROP POLICY IF EXISTS api_keys_self_access ON api_keys;
        DROP POLICY IF EXISTS api_keys_system_read_access ON api_keys;
        DROP POLICY IF EXISTS api_keys_system_update_access ON api_keys;

        CREATE POLICY api_keys_self_access ON api_keys
            FOR ALL
            TO PUBLIC
            USING (
                profile_id = COALESCE(
                    nullif(current_setting('app.current_team_id', true), '')::uuid,
                    nullif(current_setting('app.current_profile_id', true), '')::uuid
                )
            )
            WITH CHECK (
                profile_id = COALESCE(
                    nullif(current_setting('app.current_team_id', true), '')::uuid,
                    nullif(current_setting('app.current_profile_id', true), '')::uuid
                )
            );

        CREATE POLICY api_keys_system_read_access ON api_keys
            FOR SELECT
            TO PUBLIC
            USING (
                current_setting('app.tx_mode', true) = 'system'
            );

        CREATE POLICY api_keys_system_update_access ON api_keys
            FOR UPDATE
            TO PUBLIC
            USING (
                current_setting('app.tx_mode', true) = 'system'
            )
            WITH CHECK (
                current_setting('app.tx_mode', true) = 'system'
            );
    END IF;

    ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
    ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;

    DROP POLICY IF EXISTS audit_log_insert_all ON audit_log;
    DROP POLICY IF EXISTS audit_log_system_read_access ON audit_log;
    DROP POLICY IF EXISTS audit_log_self_access ON audit_log;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'team_id'
    ) THEN
        CREATE POLICY audit_log_self_access ON audit_log
            FOR SELECT
            TO PUBLIC
            USING (
                team_id = COALESCE(
                    nullif(current_setting('app.current_team_id', true), '')::uuid,
                    nullif(current_setting('app.current_profile_id', true), '')::uuid
                )
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
    ELSE
        CREATE POLICY audit_log_self_access ON audit_log
            FOR SELECT
            TO PUBLIC
            USING (
                profile_id = COALESCE(
                    nullif(current_setting('app.current_team_id', true), '')::uuid,
                    nullif(current_setting('app.current_profile_id', true), '')::uuid
                )
            );

        CREATE POLICY audit_log_insert_all ON audit_log
            FOR INSERT
            TO PUBLIC
            WITH CHECK (
                current_setting('app.tx_mode', true) IN ('team', 'profile', 'system')
                AND (
                    current_setting('app.tx_mode', true) = 'system'
                    OR profile_id IS NULL
                    OR profile_id = COALESCE(
                        nullif(current_setting('app.current_team_id', true), '')::uuid,
                        nullif(current_setting('app.current_profile_id', true), '')::uuid
                    )
                )
            );
    END IF;

    CREATE POLICY audit_log_system_read_access ON audit_log
        FOR SELECT
        TO PUBLIC
        USING (
            current_setting('app.tx_mode', true) = 'system'
        );
END $$;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    IF to_regclass('public.audit_log') IS NOT NULL THEN
        DROP POLICY IF EXISTS audit_log_insert_all ON audit_log;
        DROP POLICY IF EXISTS audit_log_system_read_access ON audit_log;
        DROP POLICY IF EXISTS audit_log_self_access ON audit_log;
        ALTER TABLE audit_log DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        DROP POLICY IF EXISTS team_profiles_system_update_access ON team_profiles;
        DROP POLICY IF EXISTS team_profiles_system_read_access ON team_profiles;
        DROP POLICY IF EXISTS team_profiles_self_access ON team_profiles;
        ALTER TABLE team_profiles DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.api_keys') IS NOT NULL THEN
        DROP POLICY IF EXISTS api_keys_system_update_access ON api_keys;
        DROP POLICY IF EXISTS api_keys_system_read_access ON api_keys;
        DROP POLICY IF EXISTS api_keys_self_access ON api_keys;
        ALTER TABLE api_keys DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.teams') IS NOT NULL THEN
        DROP POLICY IF EXISTS teams_system_read_access ON teams;
        DROP POLICY IF EXISTS teams_self_access ON teams;
        ALTER TABLE teams DISABLE ROW LEVEL SECURITY;
    END IF;

    IF to_regclass('public.profiles') IS NOT NULL THEN
        DROP POLICY IF EXISTS profiles_system_read_access ON profiles;
        DROP POLICY IF EXISTS profiles_self_access ON profiles;
        ALTER TABLE profiles DISABLE ROW LEVEL SECURITY;
    END IF;
END $$;

-- +goose StatementEnd
