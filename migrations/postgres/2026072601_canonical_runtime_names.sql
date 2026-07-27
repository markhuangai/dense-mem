-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF to_regprocedure('public.prevent_v2_append_only_mutation()') IS NOT NULL
       AND to_regprocedure('public.prevent_append_only_mutation()') IS NULL THEN
        ALTER FUNCTION prevent_v2_append_only_mutation() RENAME TO prevent_append_only_mutation;
    END IF;

    IF to_regprocedure('public.prevent_v2_reference_definition_mutation()') IS NOT NULL
       AND to_regprocedure('public.prevent_reference_definition_mutation()') IS NULL THEN
        ALTER FUNCTION prevent_v2_reference_definition_mutation() RENAME TO prevent_reference_definition_mutation;
    END IF;

    IF to_regprocedure('public.guard_v2_search_index_generation_lifecycle()') IS NOT NULL
       AND to_regprocedure('public.guard_search_index_generation_lifecycle()') IS NULL THEN
        ALTER FUNCTION guard_v2_search_index_generation_lifecycle() RENAME TO guard_search_index_generation_lifecycle;
    END IF;

    IF to_regclass('public.community_snapshot_runs') IS NOT NULL THEN
        ALTER TABLE community_snapshot_runs
            ALTER COLUMN profile_version SET DEFAULT 'postgres';

        UPDATE community_snapshot_runs
        SET profile_version = 'postgres'
        WHERE profile_version = 'postgres-v2';
    END IF;
END $$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF to_regprocedure('public.prevent_append_only_mutation()') IS NOT NULL
       AND to_regprocedure('public.prevent_v2_append_only_mutation()') IS NULL THEN
        ALTER FUNCTION prevent_append_only_mutation() RENAME TO prevent_v2_append_only_mutation;
    END IF;

    IF to_regprocedure('public.prevent_reference_definition_mutation()') IS NOT NULL
       AND to_regprocedure('public.prevent_v2_reference_definition_mutation()') IS NULL THEN
        ALTER FUNCTION prevent_reference_definition_mutation() RENAME TO prevent_v2_reference_definition_mutation;
    END IF;

    IF to_regprocedure('public.guard_search_index_generation_lifecycle()') IS NOT NULL
       AND to_regprocedure('public.guard_v2_search_index_generation_lifecycle()') IS NULL THEN
        ALTER FUNCTION guard_search_index_generation_lifecycle() RENAME TO guard_v2_search_index_generation_lifecycle;
    END IF;

    IF to_regclass('public.community_snapshot_runs') IS NOT NULL THEN
        ALTER TABLE community_snapshot_runs
            ALTER COLUMN profile_version SET DEFAULT 'postgres-v2';

        UPDATE community_snapshot_runs
        SET profile_version = 'postgres-v2'
        WHERE profile_version = 'postgres';
    END IF;
END $$;

-- +goose StatementEnd
