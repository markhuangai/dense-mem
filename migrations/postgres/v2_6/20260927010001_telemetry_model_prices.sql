-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: inserts one app_config key without rewriting existing
-- prices or usage; it may briefly lock that key during the insert.
-- RLS impact: the migration uses existing system mode and changes no policy.
-- Backfill: none; historical usage and prices retain their original meaning.
-- Backward compatibility: existing component rates remain in force unless an
-- operator configures a matching model rate under the new key.
SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

INSERT INTO app_config (key, value)
VALUES ('TELEMETRY_COST_MODEL_PRICES_JSON', '')
ON CONFLICT (key) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Rollback: Down deletes only this new setting and any operator-entered model
-- rates; existing component rates remain.
SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DELETE FROM app_config WHERE key = 'TELEMETRY_COST_MODEL_PRICES_JSON';

-- +goose StatementEnd
