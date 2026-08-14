-- +goose Up
-- Allow SSO login to create team profile rows while keeping system-mode inserts
-- scoped to SSO-linked profiles.
CREATE POLICY team_profiles_system_sso_insert_access ON team_profiles
    FOR INSERT
    TO PUBLIC
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'system'
        AND auth_source = 'sso'
        AND sso_identity_id IS NOT NULL
        AND sso_provider_id IS NOT NULL
        AND NULLIF(sso_subject, '') IS NOT NULL
    );

-- +goose Down
DROP POLICY IF EXISTS team_profiles_system_sso_insert_access ON team_profiles;
