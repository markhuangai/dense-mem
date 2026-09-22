-- +goose NO TRANSACTION
-- +goose Up

-- Preserve uniqueness for legacy rows while the run-scoped replacement is
-- attached. The catalog checks below make retries safe after any statement.
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_unique_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_unscoped_unique_idx_invalid;

-- A failed concurrent build leaves an invalid index under its canonical name.
-- Park those entries so the replacement can be built on the next attempt.
-- +goose StatementBegin
DO $dream_path_evaluations_index_recovery$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS index_state
        JOIN pg_class AS index_class ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname IN (
              'dream_path_evaluations_run_unique_idx',
              'dream_path_evaluations_unscoped_unique_idx'
          )
          AND index_state.indisvalid IS FALSE
    LOOP
        EXECUTE format(
            'ALTER INDEX public.%I RENAME TO %I',
            candidate.relname,
            candidate.relname || '_invalid'
        );
    END LOOP;
END
$dream_path_evaluations_index_recovery$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dream_path_evaluations_run_unique_idx
    ON dream_path_evaluations(
        team_id, first_relationship_id, first_relationship_version,
        second_relationship_id, second_relationship_version,
        allowed_predicate_fingerprint, run_id
    );

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dream_path_evaluations_unscoped_unique_idx
    ON dream_path_evaluations(
        team_id, first_relationship_id, first_relationship_version,
        second_relationship_id, second_relationship_version,
        allowed_predicate_fingerprint
    )
    WHERE run_id IS NULL;

-- +goose StatementBegin
DO $$
DECLARE
    constraint_uses_run_id boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM pg_constraint constraint_row
        WHERE constraint_row.conrelid = 'dream_path_evaluations'::regclass
          AND constraint_row.conname = 'dream_path_evaluations_exact_path_unique'
          AND constraint_row.contype = 'u'
          AND EXISTS (
              SELECT 1
              FROM unnest(constraint_row.conkey) AS key_column(attnum)
              JOIN pg_attribute attribute_row
                ON attribute_row.attrelid = constraint_row.conrelid
               AND attribute_row.attnum = key_column.attnum
              WHERE attribute_row.attname = 'run_id'
          )
    )
    INTO constraint_uses_run_id;

    IF NOT constraint_uses_run_id THEN
        ALTER TABLE dream_path_evaluations
            DROP CONSTRAINT IF EXISTS dream_path_evaluations_exact_path_unique;

        ALTER TABLE dream_path_evaluations
            ADD CONSTRAINT dream_path_evaluations_exact_path_unique
            UNIQUE USING INDEX dream_path_evaluations_run_unique_idx;
    END IF;
END $$;
-- +goose StatementEnd

-- ADD CONSTRAINT ... USING INDEX renames the temporary index. If the migration
-- was retried after that attachment, remove the newly recreated temporary copy.
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_unique_idx;
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_run_unique_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS dream_path_evaluations_unscoped_unique_idx_invalid;

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION '20260921010004_scope_dream_path_evaluations_to_run is irreversible: run-scoped path history is append-only';
END $$;
-- +goose StatementEnd
