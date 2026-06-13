-- +goose Up
-- +goose StatementBegin

INSERT INTO app_config (key, value)
VALUES
    ('DREAMING_ENABLED', 'false'),
    ('DREAMING_FORCE_ENABLED', 'false'),
    ('DREAMING_START_TIME_LOCAL', '03:00'),
    ('DREAMING_TIMEZONE', 'UTC'),
    ('DREAMING_REFLECT_ENABLED', 'true'),
    ('DREAMING_REEVALUATE_ENABLED', 'true'),
    ('DREAMING_DREAM_ENABLED', 'true'),
    ('DREAMING_MODEL', ''),
    ('DREAMING_MAX_OUTPUTS', '5')
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

DELETE FROM app_config
WHERE key IN (
    'DREAMING_ENABLED',
    'DREAMING_FORCE_ENABLED',
    'DREAMING_START_TIME_LOCAL',
    'DREAMING_TIMEZONE',
    'DREAMING_REFLECT_ENABLED',
    'DREAMING_REEVALUATE_ENABLED',
    'DREAMING_DREAM_ENABLED',
    'DREAMING_MODEL',
    'DREAMING_MAX_OUTPUTS'
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
