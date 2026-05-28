-- +goose Up
-- +goose StatementBegin

-- audit_log is append-only. Foreign-key ON DELETE SET NULL would mutate
-- historical audit rows when teams or team profiles are hard-deleted, which
-- conflicts with the audit_log_append_only trigger.
ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_team_id_fkey;
ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_actor_profile_id_fkey;
ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_profile_id_fkey;
ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_actor_key_id_fkey;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

-- Recreate the original constraints for rollback. NOT VALID avoids failing if
-- hard-deleted teams or profiles have left historical audit references behind.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'team_id'
    ) AND to_regclass('public.teams') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'audit_log_team_id_fkey'
    ) THEN
        ALTER TABLE audit_log
            ADD CONSTRAINT audit_log_team_id_fkey
            FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE SET NULL NOT VALID;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'actor_profile_id'
    ) AND to_regclass('public.team_profiles') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'audit_log_actor_profile_id_fkey'
    ) THEN
        ALTER TABLE audit_log
            ADD CONSTRAINT audit_log_actor_profile_id_fkey
            FOREIGN KEY (actor_profile_id) REFERENCES team_profiles(id) ON DELETE SET NULL NOT VALID;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'profile_id'
    ) AND to_regclass('public.profiles') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'audit_log_profile_id_fkey'
    ) THEN
        ALTER TABLE audit_log
            ADD CONSTRAINT audit_log_profile_id_fkey
            FOREIGN KEY (profile_id) REFERENCES profiles(id) ON DELETE SET NULL NOT VALID;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'actor_key_id'
    ) AND to_regclass('public.api_keys') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'audit_log_actor_key_id_fkey'
    ) THEN
        ALTER TABLE audit_log
            ADD CONSTRAINT audit_log_actor_key_id_fkey
            FOREIGN KEY (actor_key_id) REFERENCES api_keys(id) ON DELETE SET NULL NOT VALID;
    END IF;
END $$;

-- +goose StatementEnd
