-- +goose NO TRANSACTION
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
            REFERENCES dream_cycle_runs(team_id, run_id) ON DELETE RESTRICT NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE dream_path_evaluations
    VALIDATE CONSTRAINT dream_path_evaluations_run_fk;

DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_idx_invalid;

-- A failed concurrent build leaves an invalid index under its canonical name.
-- Park it so a later migration attempt can rebuild the lookup without blocking writes.
-- +goose StatementBegin
DO $dream_path_evaluations_run_index_recovery$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_index AS index_state
        JOIN pg_class AS index_class ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname = 'dream_path_evaluations_run_idx'
          AND index_state.indisvalid IS FALSE
    ) THEN
        ALTER INDEX public.dream_path_evaluations_run_idx
            RENAME TO dream_path_evaluations_run_idx_invalid;
    END IF;
END
$dream_path_evaluations_run_index_recovery$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS dream_path_evaluations_run_idx
    ON dream_path_evaluations(team_id, run_id, created_at DESC)
    WHERE run_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_idx_invalid;

-- +goose Down

ALTER TABLE dream_path_evaluations
    DROP CONSTRAINT IF EXISTS dream_path_evaluations_run_fk;
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_idx;
ALTER TABLE dream_path_evaluations
    DROP COLUMN IF EXISTS run_id;
