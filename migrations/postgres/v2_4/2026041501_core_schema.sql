-- +goose Up
-- +goose StatementBegin

-- Enable pgcrypto extension for gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Teams table: stores tenant-like entities for multi-tenancy
CREATE TABLE IF NOT EXISTS teams (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ NULL
);

-- Partial unique index: only active (non-deleted) profiles must have unique names
CREATE UNIQUE INDEX IF NOT EXISTS idx_teams_name_unique_active
    ON teams (lower(name))
    WHERE deleted_at IS NULL;

-- Team profiles table: stores named profile identities and their active API key material
CREATE TABLE IF NOT EXISTS team_profiles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    key_hash TEXT NOT NULL,
    key_prefix VARCHAR(24) NOT NULL,
    key_suffix VARCHAR(6) NULL,
    name VARCHAR(100) NOT NULL DEFAULT '',
    scopes TEXT[] NOT NULL DEFAULT ARRAY['read','write']::text[],
    role VARCHAR(20) NOT NULL DEFAULT 'member' CHECK (role IN ('manager', 'member')),
    rate_limit INTEGER NOT NULL DEFAULT 0 CHECK (rate_limit >= 0),
    expires_at TIMESTAMPTZ NULL,
    revoked_at TIMESTAMPTZ NULL,
    last_used_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes for team_profiles
CREATE INDEX IF NOT EXISTS idx_team_profiles_team_id ON team_profiles(team_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_key_prefix_unique ON team_profiles(key_prefix);
CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_team_name_unique ON team_profiles(team_id, lower(name));

-- Audit log table: append-only record of all operations
CREATE TABLE IF NOT EXISTS audit_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NULL REFERENCES teams(id) ON DELETE SET NULL,
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    operation VARCHAR(64) NOT NULL,
    entity_type VARCHAR(64) NOT NULL,
    entity_id TEXT NOT NULL,
    before_payload JSONB NULL,
    after_payload JSONB NULL,
    actor_profile_id UUID NULL REFERENCES team_profiles(id) ON DELETE SET NULL,
    actor_role VARCHAR(20) NULL,
    client_ip INET NULL,
    correlation_id TEXT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb
);

-- Indexes for audit_log. Existing deployments may still have profile_id until
-- the team/profile rename migration runs.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'team_id'
    ) THEN
        CREATE INDEX IF NOT EXISTS idx_audit_log_team_timestamp ON audit_log(team_id, timestamp DESC);
    ELSIF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'profile_id'
    ) THEN
        CREATE INDEX IF NOT EXISTS idx_audit_log_profile_timestamp ON audit_log(profile_id, timestamp DESC);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp DESC);

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

-- Drop tables in reverse dependency order to avoid FK constraint errors
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS team_profiles;
DROP TABLE IF EXISTS teams;

-- +goose StatementEnd
