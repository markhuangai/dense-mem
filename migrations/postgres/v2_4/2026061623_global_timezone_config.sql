-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

WITH existing_custom_timezone AS (
    SELECT value
    FROM app_config
    WHERE key LIKE '%\_TIMEZONE' ESCAPE '\'
      AND key <> 'APP_TIMEZONE'
      AND value NOT IN ('', 'UTC', 'Local')
    ORDER BY updated_at DESC, key ASC
    LIMIT 1
)
INSERT INTO app_config (key, value)
VALUES ('APP_TIMEZONE', COALESCE((SELECT value FROM existing_custom_timezone), 'Local'))
ON CONFLICT (key) DO UPDATE
SET value = EXCLUDED.value,
    updated_at = clock_timestamp()
WHERE app_config.value IN ('', 'UTC', 'Local');

DELETE FROM app_config
WHERE key LIKE '%\_TIMEZONE' ESCAPE '\'
  AND key <> 'APP_TIMEZONE';

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

-- APP_TIMEZONE is baseline app_config state; removed per-feature timezone keys are not restored.

-- +goose StatementEnd
