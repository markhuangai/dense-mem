-- +goose Up
-- +goose StatementBegin

-- Inserts one operator setting without rewriting existing prices or usage.
-- RLS policy is unchanged; the migration uses the existing system mode.
SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

INSERT INTO app_config (key, value)
VALUES ('TELEMETRY_COST_MODEL_PRICES_JSON', '')
ON CONFLICT (key) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Rollback deletes only this new setting and any operator-entered model rates.
SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DELETE FROM app_config WHERE key = 'TELEMETRY_COST_MODEL_PRICES_JSON';

-- +goose StatementEnd
