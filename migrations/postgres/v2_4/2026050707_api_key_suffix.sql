-- +goose Up
-- +goose StatementBegin

DO $$
BEGIN
    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles ADD COLUMN IF NOT EXISTS key_suffix VARCHAR(6) NULL;
    ELSIF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_suffix VARCHAR(6) NULL;
    END IF;
END $$;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles DROP COLUMN IF EXISTS key_suffix;
    ELSIF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys DROP COLUMN IF EXISTS key_suffix;
    END IF;
END $$;

-- +goose StatementEnd
