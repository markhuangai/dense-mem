-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE memory_placement_items
    ADD COLUMN IF NOT EXISTS owner_profile_id TEXT NOT NULL DEFAULT '';

ALTER TABLE memory_placement_runs
    ADD COLUMN IF NOT EXISTS owner_profile_id TEXT NOT NULL DEFAULT '';

UPDATE memory_placement_runs
SET owner_profile_id = profile_id::text
WHERE owner_profile_id = '';

UPDATE memory_placement_items
SET owner_profile_id = profile_id::text
WHERE owner_profile_id = '';

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

ALTER TABLE memory_placement_items
    DROP COLUMN IF EXISTS owner_profile_id;

ALTER TABLE memory_placement_runs
    DROP COLUMN IF EXISTS owner_profile_id;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
