-- +goose Up
ALTER TABLE team_profiles
    ADD COLUMN IF NOT EXISTS sso_owner_identity_id UUID NULL REFERENCES sso_identities(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_sso_owner_team_active_unique
    ON team_profiles(sso_owner_identity_id, team_id)
    WHERE sso_owner_identity_id IS NOT NULL
        AND auth_source = 'api_key'
        AND revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_team_profiles_sso_owner_identity
    ON team_profiles(sso_owner_identity_id)
    WHERE sso_owner_identity_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_team_profiles_sso_owner_identity;
DROP INDEX IF EXISTS idx_team_profiles_sso_owner_team_active_unique;

ALTER TABLE team_profiles
    DROP COLUMN IF EXISTS sso_owner_identity_id;
