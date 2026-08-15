-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- This metadata-only drop takes an ACCESS EXCLUSIVE lock and intentionally
-- discards historical values. Deploy it only after every old writer is stopped.
ALTER TABLE evidence_security_events
    DROP COLUMN scan_policy_hash;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Rollback restores compatibility structure, not discarded historical values.
ALTER TABLE evidence_security_events
    ADD COLUMN scan_policy_hash TEXT NOT NULL DEFAULT '';

-- +goose StatementEnd
