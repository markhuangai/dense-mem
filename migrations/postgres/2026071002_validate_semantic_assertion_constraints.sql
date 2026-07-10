-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE memory_placement_runs
    VALIDATE CONSTRAINT memory_placement_runs_status_check;
ALTER TABLE memory_placement_items
    VALIDATE CONSTRAINT memory_placement_items_category_check;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Validation changes only constraint metadata; the owning migration removes or restores each constraint.
SELECT 1;

-- +goose StatementEnd
