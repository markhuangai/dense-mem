-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

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

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Migration 2026080602 already defines this policy, so rollback leaves it intact.

-- +goose StatementEnd
