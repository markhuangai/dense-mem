-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE recall_feedback_events
    ADD COLUMN feedback_comment TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT recall_feedback_events_feedback_comment_length_check
        CHECK (char_length(feedback_comment) <= 1000);

UPDATE recall_feedback_events
SET feedback_comment = CASE
    WHEN expected_context <> '' THEN expected_context
    ELSE failure_reason
END
WHERE feedback_comment = '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE recall_feedback_events
    DROP CONSTRAINT IF EXISTS recall_feedback_events_feedback_comment_length_check,
    DROP COLUMN IF EXISTS feedback_comment;

-- +goose StatementEnd
