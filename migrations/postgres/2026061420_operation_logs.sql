-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS operation_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
    severity VARCHAR(16) NOT NULL,
    severity_rank SMALLINT NOT NULL,
    message TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    team_id UUID NULL,
    profile_id UUID NULL,
    correlation_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    attrs JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_operation_logs_timestamp
    ON operation_logs(timestamp DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_operation_logs_severity_timestamp
    ON operation_logs(severity_rank DESC, timestamp DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_operation_logs_severity_value_timestamp
    ON operation_logs(severity, timestamp DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_operation_logs_team_timestamp
    ON operation_logs(team_id, timestamp DESC)
    WHERE team_id IS NOT NULL;

ALTER TABLE operation_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE operation_logs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS operation_logs_system_access ON operation_logs;
CREATE POLICY operation_logs_system_access ON operation_logs
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

INSERT INTO app_config (key, value)
VALUES ('OPERATION_LOG_RETENTION_DAYS', '30')
ON CONFLICT (key) DO NOTHING;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DELETE FROM app_config
WHERE key = 'OPERATION_LOG_RETENTION_DAYS';

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

DROP TABLE IF EXISTS operation_logs;

-- +goose StatementEnd
