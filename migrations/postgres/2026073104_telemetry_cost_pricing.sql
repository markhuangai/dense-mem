-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite: this inserts three config rows after the legacy skip repair and updates update_time; it
-- performs no table rewrite. Historical marker recovery is intentionally
-- performed in bounded, retryable system transactions by the application so a
-- migration does not hold an unbounded transaction on placement history.
-- RLS policies are unchanged because the migration uses the existing system
-- transaction mode.
SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

INSERT INTO app_config (key, value)
VALUES
    ('TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS', ''),
    ('TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS', ''),
    ('TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS', '')
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
    'TELEMETRY_COST_VERIFIER_INPUT_USD_PER_MILLION_TOKENS',
    'TELEMETRY_COST_VERIFIER_OUTPUT_USD_PER_MILLION_TOKENS',
    'TELEMETRY_COST_EMBEDDING_INPUT_USD_PER_MILLION_TOKENS'
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
