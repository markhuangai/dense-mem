-- +goose NO TRANSACTION
-- +goose Up

-- The old path identity remains authoritative for unscoped history. Build the
-- run-scoped replacement without blocking normal path reads and writes, then
-- swap the catalog constraint while the old constraint is still enforcing the
-- legacy key.
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_unique_idx;

CREATE UNIQUE INDEX CONCURRENTLY dream_path_evaluations_run_unique_idx
    ON dream_path_evaluations(
        team_id, first_relationship_id, first_relationship_version,
        second_relationship_id, second_relationship_version,
        allowed_predicate_fingerprint, run_id
    );

ALTER TABLE dream_path_evaluations
    DROP CONSTRAINT IF EXISTS dream_path_evaluations_exact_path_unique;

ALTER TABLE dream_path_evaluations
    ADD CONSTRAINT dream_path_evaluations_exact_path_unique
    UNIQUE USING INDEX dream_path_evaluations_run_unique_idx;

-- +goose Down

DO $$
BEGIN
    RAISE EXCEPTION '20260921010004_scope_dream_path_evaluations_to_run is irreversible: run-scoped path history is append-only';
END $$;
