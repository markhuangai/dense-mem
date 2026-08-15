-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

INSERT INTO app_config (key, value)
VALUES
    ('COMMUNITY_DETECTION_ENABLED', 'false'),
    ('COMMUNITY_DETECTION_START_TIME_LOCAL', '03:30'),
    ('COMMUNITY_DETECTION_MAX_CONCURRENCY', '1'),
    ('COMMUNITY_DETECTION_JITTER_SECONDS', '600')
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
WHERE key IN (
    'COMMUNITY_DETECTION_ENABLED',
    'COMMUNITY_DETECTION_START_TIME_LOCAL',
    'COMMUNITY_DETECTION_MAX_CONCURRENCY',
    'COMMUNITY_DETECTION_JITTER_SECONDS'
);

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
