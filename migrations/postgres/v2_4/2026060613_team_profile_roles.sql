-- +goose Up
-- +goose StatementBegin

ALTER TABLE team_profiles
    ADD COLUMN IF NOT EXISTS role VARCHAR(20) NOT NULL DEFAULT 'member';

ALTER TABLE team_profiles
    ALTER COLUMN role SET DEFAULT 'member';

UPDATE team_profiles
SET role = 'member'
WHERE role IS NULL OR role NOT IN ('manager', 'member');

ALTER TABLE team_profiles
    ALTER COLUMN role SET NOT NULL;

ALTER TABLE team_profiles
    DROP CONSTRAINT IF EXISTS team_profiles_role_check;

ALTER TABLE team_profiles
    ADD CONSTRAINT team_profiles_role_check
    CHECK (role IN ('manager', 'member'));

WITH ranked_active AS (
    SELECT
        id,
        row_number() OVER (
            PARTITION BY team_id
            ORDER BY created_at ASC, id ASC
        ) AS rn
    FROM team_profiles
    WHERE revoked_at IS NULL
        AND (expires_at IS NULL OR expires_at > now())
)
UPDATE team_profiles tp
SET role = CASE WHEN ranked_active.rn = 1 THEN 'manager' ELSE 'member' END
FROM ranked_active
WHERE tp.id = ranked_active.id;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

ALTER TABLE team_profiles DROP CONSTRAINT IF EXISTS team_profiles_role_check;
ALTER TABLE team_profiles DROP COLUMN IF EXISTS role;

-- +goose StatementEnd
