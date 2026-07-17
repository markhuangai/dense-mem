-- +goose Up
-- +goose StatementBegin

ALTER TABLE relationship_transition_events
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';

ALTER TABLE relationship_support_decision_events
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';

ALTER TABLE placement_outcomes
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS relationship_transition_events_idempotency_unique
    ON relationship_transition_events(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '';

CREATE UNIQUE INDEX IF NOT EXISTS relationship_support_decision_events_idempotency_unique
    ON relationship_support_decision_events(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '';

CREATE UNIQUE INDEX IF NOT EXISTS placement_outcomes_idempotency_unique
    ON placement_outcomes(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS placement_outcomes_idempotency_unique;

DROP INDEX IF EXISTS relationship_support_decision_events_idempotency_unique;

DROP INDEX IF EXISTS relationship_transition_events_idempotency_unique;

ALTER TABLE placement_outcomes
    DROP COLUMN IF EXISTS idempotency_key;

ALTER TABLE relationship_support_decision_events
    DROP COLUMN IF EXISTS idempotency_key;

ALTER TABLE relationship_transition_events
    DROP COLUMN IF EXISTS idempotency_key;

-- +goose StatementEnd
