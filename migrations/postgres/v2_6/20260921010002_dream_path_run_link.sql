-- +goose Up

ALTER TABLE dream_path_evaluations
    ADD COLUMN IF NOT EXISTS run_id UUID NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'dream_path_evaluations_run_fk'
    ) THEN
        ALTER TABLE dream_path_evaluations
            ADD CONSTRAINT dream_path_evaluations_run_fk
            FOREIGN KEY (team_id, run_id)
            REFERENCES dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT;
    END IF;
END $$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS dream_path_evaluations_run_idx
    ON dream_path_evaluations(team_id, run_id, created_at DESC)
    WHERE run_id IS NOT NULL;

-- +goose Down

ALTER TABLE dream_path_evaluations
    DROP CONSTRAINT IF EXISTS dream_path_evaluations_run_fk;
DROP INDEX IF EXISTS dream_path_evaluations_run_idx;
ALTER TABLE dream_path_evaluations
    DROP COLUMN IF EXISTS run_id;
