-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE recall_feedback_events
    ADD COLUMN failure_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN expected_context TEXT NOT NULL DEFAULT '',
    ADD COLUMN irrelevant_result_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT recall_feedback_events_failure_reason_length_check
        CHECK (char_length(failure_reason) <= 1000),
    ADD CONSTRAINT recall_feedback_events_expected_context_length_check
        CHECK (char_length(expected_context) <= 1000),
    ADD CONSTRAINT recall_feedback_events_irrelevant_result_refs_array_check
        CHECK (
            CASE
                WHEN jsonb_typeof(irrelevant_result_refs) = 'array'
                THEN jsonb_array_length(irrelevant_result_refs) <= 20
                ELSE false
            END
        );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE recall_feedback_events
    DROP CONSTRAINT IF EXISTS recall_feedback_events_irrelevant_result_refs_array_check,
    DROP CONSTRAINT IF EXISTS recall_feedback_events_expected_context_length_check,
    DROP CONSTRAINT IF EXISTS recall_feedback_events_failure_reason_length_check,
    DROP COLUMN IF EXISTS irrelevant_result_refs,
    DROP COLUMN IF EXISTS expected_context,
    DROP COLUMN IF EXISTS failure_reason;

-- +goose StatementEnd
