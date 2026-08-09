-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE relationship_conflict_events
    VALIDATE CONSTRAINT relationship_conflict_events_action_check;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION '2026080903_validate_conflict_policy_events is irreversible because constraint validation is part of the policy rollout';
END $$;

-- +goose StatementEnd
