-- +goose Up
-- +goose StatementBegin

DO $$
BEGIN
    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles ALTER COLUMN scopes SET DEFAULT ARRAY['read','write']::text[];
        UPDATE team_profiles SET scopes = ARRAY['read','write']::text[] WHERE scopes <> ARRAY['read','write']::text[];
    ELSIF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys ALTER COLUMN scopes SET DEFAULT ARRAY['read','write']::text[];
        UPDATE api_keys SET scopes = ARRAY['read','write']::text[] WHERE scopes <> ARRAY['read','write']::text[];
    END IF;
END $$;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    IF to_regclass('public.team_profiles') IS NOT NULL THEN
        ALTER TABLE team_profiles ALTER COLUMN scopes SET DEFAULT ARRAY['read']::text[];
    ELSIF to_regclass('public.api_keys') IS NOT NULL THEN
        ALTER TABLE api_keys ALTER COLUMN scopes SET DEFAULT ARRAY['read']::text[];
    END IF;
END $$;

-- +goose StatementEnd
