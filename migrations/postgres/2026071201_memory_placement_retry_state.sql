-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE memory_placement_runs
    ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS available_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE memory_placement_runs
    DROP CONSTRAINT IF EXISTS memory_placement_runs_attempts_check;

ALTER TABLE memory_placement_runs
    ADD CONSTRAINT memory_placement_runs_attempts_check
    CHECK (attempts >= 0);

CREATE INDEX IF NOT EXISTS idx_memory_placement_runs_status_available
    ON memory_placement_runs(status, available_at ASC, created_at ASC);

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

DROP INDEX IF EXISTS idx_memory_placement_runs_status_available;

ALTER TABLE memory_placement_runs
    DROP CONSTRAINT IF EXISTS memory_placement_runs_attempts_check;

ALTER TABLE memory_placement_runs
    DROP COLUMN IF EXISTS available_at,
    DROP COLUMN IF EXISTS attempts;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
