-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_roles
        WHERE rolname = 'dense_mem_portal_session_system'
    ) THEN
        CREATE ROLE dense_mem_portal_session_system NOLOGIN NOSUPERUSER NOBYPASSRLS;
    ELSE
        ALTER ROLE dense_mem_portal_session_system
            WITH NOLOGIN NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

REVOKE ALL ON user_portal_sessions FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO dense_mem_portal_session_system;
GRANT SELECT, INSERT, UPDATE, DELETE ON user_portal_sessions
    TO dense_mem_portal_session_system;
GRANT REFERENCES ON team_profiles TO dense_mem_portal_session_system;

DROP POLICY IF EXISTS user_portal_sessions_system_access ON user_portal_sessions;
CREATE POLICY user_portal_sessions_system_access ON user_portal_sessions
    FOR ALL
    TO dense_mem_portal_session_system
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

CREATE OR REPLACE FUNCTION public.dense_mem_portal_session_create(
    p_session_hash TEXT,
    p_key_id UUID,
    p_csrf_hash TEXT,
    p_expires_at TIMESTAMPTZ,
    p_created_at TIMESTAMPTZ
)
RETURNS VOID
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    INSERT INTO public.user_portal_sessions (
        session_hash, key_id, csrf_hash, expires_at, created_at
    )
    VALUES ($1, $2, $3, $4, $5);
$$;

CREATE OR REPLACE FUNCTION public.dense_mem_portal_session_get(
    p_session_hash TEXT
)
RETURNS TABLE (
    session_hash TEXT,
    key_id UUID,
    csrf_hash TEXT,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT s.session_hash, s.key_id, s.csrf_hash, s.expires_at, s.created_at
    FROM public.user_portal_sessions AS s
    WHERE s.session_hash = $1
      AND s.expires_at > NOW();
$$;

CREATE OR REPLACE FUNCTION public.dense_mem_portal_session_delete(
    p_session_hash TEXT
)
RETURNS VOID
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    DELETE FROM public.user_portal_sessions
    WHERE session_hash = $1;
$$;

CREATE OR REPLACE FUNCTION public.dense_mem_portal_session_delete_expired(
    p_expires_at TIMESTAMPTZ
)
RETURNS VOID
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    DELETE FROM public.user_portal_sessions
    WHERE expires_at <= $1;
$$;

REVOKE ALL ON FUNCTION public.dense_mem_portal_session_create(
    TEXT, UUID, TEXT, TIMESTAMPTZ, TIMESTAMPTZ
) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.dense_mem_portal_session_create(
    TEXT, UUID, TEXT, TIMESTAMPTZ, TIMESTAMPTZ
) TO CURRENT_USER;
ALTER FUNCTION public.dense_mem_portal_session_create(
    TEXT, UUID, TEXT, TIMESTAMPTZ, TIMESTAMPTZ
) OWNER TO dense_mem_portal_session_system;

REVOKE ALL ON FUNCTION public.dense_mem_portal_session_get(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.dense_mem_portal_session_get(TEXT) TO CURRENT_USER;
ALTER FUNCTION public.dense_mem_portal_session_get(TEXT)
    OWNER TO dense_mem_portal_session_system;

REVOKE ALL ON FUNCTION public.dense_mem_portal_session_delete(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.dense_mem_portal_session_delete(TEXT) TO CURRENT_USER;
ALTER FUNCTION public.dense_mem_portal_session_delete(TEXT)
    OWNER TO dense_mem_portal_session_system;

REVOKE ALL ON FUNCTION public.dense_mem_portal_session_delete_expired(TIMESTAMPTZ)
    FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.dense_mem_portal_session_delete_expired(TIMESTAMPTZ)
    TO CURRENT_USER;
ALTER FUNCTION public.dense_mem_portal_session_delete_expired(TIMESTAMPTZ)
    OWNER TO dense_mem_portal_session_system;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP FUNCTION IF EXISTS public.dense_mem_portal_session_create(
    TEXT, UUID, TEXT, TIMESTAMPTZ, TIMESTAMPTZ
);
DROP FUNCTION IF EXISTS public.dense_mem_portal_session_get(TEXT);
DROP FUNCTION IF EXISTS public.dense_mem_portal_session_delete(TEXT);
DROP FUNCTION IF EXISTS public.dense_mem_portal_session_delete_expired(TIMESTAMPTZ);

DROP POLICY IF EXISTS user_portal_sessions_system_access ON user_portal_sessions;
CREATE POLICY user_portal_sessions_system_access ON user_portal_sessions
    FOR ALL
    TO PUBLIC
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

REVOKE ALL ON user_portal_sessions FROM dense_mem_portal_session_system;
REVOKE ALL ON SCHEMA public FROM dense_mem_portal_session_system;
DROP ROLE IF EXISTS dense_mem_portal_session_system;

-- +goose StatementEnd
