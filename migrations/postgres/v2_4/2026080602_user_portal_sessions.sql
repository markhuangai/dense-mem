-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS user_portal_sessions (
    session_hash TEXT PRIMARY KEY,
    key_id UUID NOT NULL REFERENCES team_profiles(id) ON DELETE CASCADE,
    csrf_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_portal_sessions_key_id
    ON user_portal_sessions(key_id);

CREATE INDEX IF NOT EXISTS idx_user_portal_sessions_expires_at
    ON user_portal_sessions(expires_at);

ALTER TABLE user_portal_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_portal_sessions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS user_portal_sessions_system_access ON user_portal_sessions;
CREATE POLICY user_portal_sessions_system_access ON user_portal_sessions
    FOR ALL
    TO PUBLIC
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS user_portal_sessions;

-- +goose StatementEnd
