-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE recall_feedback_events
    ADD COLUMN failure_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN expected_context TEXT NOT NULL DEFAULT '',
    ADD COLUMN irrelevant_result_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT recall_feedback_events_irrelevant_result_refs_array_check
        CHECK (jsonb_typeof(irrelevant_result_refs) = 'array');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE recall_feedback_events
    DROP CONSTRAINT IF EXISTS recall_feedback_events_irrelevant_result_refs_array_check,
    DROP COLUMN IF EXISTS irrelevant_result_refs,
    DROP COLUMN IF EXISTS expected_context,
    DROP COLUMN IF EXISTS failure_reason;

-- +goose StatementEnd
