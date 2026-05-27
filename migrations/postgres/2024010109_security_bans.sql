-- +goose Up
-- +goose StatementBegin

ALTER TABLE api_keys
    ALTER COLUMN key_prefix TYPE VARCHAR(24);

CREATE TABLE IF NOT EXISTS security_settings (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    enabled BOOLEAN NOT NULL DEFAULT false,
    failure_threshold INTEGER NOT NULL DEFAULT 10 CHECK (failure_threshold > 0),
    failure_window_seconds INTEGER NOT NULL DEFAULT 600 CHECK (failure_window_seconds > 0),
    ban_duration_seconds INTEGER NOT NULL DEFAULT 0 CHECK (ban_duration_seconds >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO security_settings (id)
VALUES (true)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS security_ip_failures (
    ip TEXT PRIMARY KEY,
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    first_failed_at TIMESTAMPTZ NOT NULL,
    last_failed_at TIMESTAMPTZ NOT NULL,
    last_reason TEXT NOT NULL DEFAULT '',
    last_surface TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS security_ip_bans (
    ip TEXT PRIMARY KEY,
    reason TEXT NOT NULL,
    source VARCHAR(16) NOT NULL CHECK (source IN ('auto', 'manual')),
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    banned_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NULL,
    last_failed_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_security_ip_bans_active
    ON security_ip_bans(ip)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_security_ip_bans_expires_at
    ON security_ip_bans(expires_at)
    WHERE expires_at IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS security_ip_bans;
DROP TABLE IF EXISTS security_ip_failures;
DROP TABLE IF EXISTS security_settings;

ALTER TABLE api_keys
    ALTER COLUMN key_prefix TYPE VARCHAR(12)
    USING left(key_prefix, 12);

-- +goose StatementEnd
