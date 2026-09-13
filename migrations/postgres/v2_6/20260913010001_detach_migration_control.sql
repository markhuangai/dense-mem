-- Lock/rewrite impact: the migration first takes bounded ACCESS EXCLUSIVE locks
-- on the three tables modified by DDL, then SHARE ROW EXCLUSIVE locks on the
-- affected retained and migration-control tables used for validation. It changes
-- metadata only; no table heap or retained row is rewritten.
-- RLS impact: migration mode is used for catalog inspection and DDL. Existing
-- policies and transaction-local application context remain unchanged.
-- Backfill: none. The migration removes only retired lineage columns, indexes,
-- and foreign keys after exact catalog validation.
-- Backward compatibility: the eight migration-control tables and their rows,
-- compatibility markers, and all canonical application state remain available.
-- Rollback: the boundary is irreversible in application code; Down refuses to
-- recreate the detached schema. Recovery uses a verified backup restoration or
-- a new ordered recovery migration after all nodes are stopped.

-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '30s';
SET LOCAL quote_all_identifiers = off;

LOCK TABLE public.knowledge_ingests,
           public.v2_compatibility_markers,
           public.v2_migration_corpus_items
    IN ACCESS EXCLUSIVE MODE;

LOCK TABLE public.v2_migration_runs,
           public.v2_migration_source_maps,
           public.v2_migration_checkpoints,
           public.v2_migration_errors,
           public.v2_migration_exclusions,
           public.v2_migration_gate_results,
           public.v2_migration_operator_actions,
           public.teams,
           public.ownership_aliases
    IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    retired_tables CONSTANT text[] := ARRAY[
        'v2_migration_runs',
        'v2_migration_corpus_items',
        'v2_migration_source_maps',
        'v2_migration_checkpoints',
        'v2_migration_errors',
        'v2_migration_exclusions',
        'v2_migration_gate_results',
        'v2_migration_operator_actions'
    ];
    detached_constraints CONSTANT text[] := ARRAY[
        'knowledge_ingests_migration_run_id_fkey',
        'v2_compatibility_markers_run_id_fkey',
        'v2_migration_corpus_items_team_id_fkey',
        'v2_migration_corpus_items_team_id_owner_profile_id_fkey',
        'v2_migration_corpus_items_team_id_ingest_id_fkey',
        'v2_migration_corpus_items_team_id_placement_item_id_fkey'
    ];
    table_name text;
    dependency record;
    unexpected text;
    ingest_team_attnum smallint;
    ingest_migration_attnum smallint;
    marker_run_attnum smallint;
    corpus_team_attnum smallint;
    corpus_owner_attnum smallint;
    corpus_ingest_attnum smallint;
    corpus_placement_item_attnum smallint;
    alias_team_attnum smallint;
    alias_owner_attnum smallint;
    placement_items_exists boolean;
    placement_team_attnum smallint;
    placement_item_attnum smallint;
    run_id_attnum smallint;
    uuid_btree_opclass oid;
    index_count bigint;
BEGIN
    FOREACH table_name IN ARRAY retired_tables LOOP
        IF to_regclass(format('public.%I', table_name)) IS NULL THEN
            RAISE EXCEPTION
                'migration-control detachment blocked: expected table public.% is missing',
                table_name;
        END IF;
    END LOOP;

    SELECT attnum INTO ingest_team_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.knowledge_ingests'::regclass
       AND attname = 'team_id'
       AND NOT attisdropped;
    SELECT attnum INTO ingest_migration_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.knowledge_ingests'::regclass
       AND attname = 'migration_run_id'
       AND NOT attisdropped;
    SELECT attnum INTO marker_run_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.v2_compatibility_markers'::regclass
       AND attname = 'run_id'
       AND NOT attisdropped;
    SELECT attnum INTO corpus_team_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.v2_migration_corpus_items'::regclass
       AND attname = 'team_id'
       AND NOT attisdropped;
    SELECT attnum INTO corpus_owner_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.v2_migration_corpus_items'::regclass
       AND attname = 'owner_profile_id'
       AND NOT attisdropped;
    SELECT attnum INTO corpus_ingest_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.v2_migration_corpus_items'::regclass
       AND attname = 'ingest_id'
       AND NOT attisdropped;
    SELECT attnum INTO corpus_placement_item_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.v2_migration_corpus_items'::regclass
       AND attname = 'placement_item_id'
       AND NOT attisdropped;
    SELECT attnum INTO alias_team_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.ownership_aliases'::regclass
       AND attname = 'team_id'
       AND NOT attisdropped;
    SELECT attnum INTO alias_owner_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.ownership_aliases'::regclass
       AND attname = 'legacy_owner_id'
       AND NOT attisdropped;
    SELECT to_regclass('public.placement_items') IS NOT NULL
      INTO placement_items_exists;
    IF placement_items_exists THEN
        SELECT attnum INTO placement_team_attnum
          FROM pg_attribute
         WHERE attrelid = 'public.placement_items'::regclass
           AND attname = 'team_id'
           AND NOT attisdropped;
        SELECT attnum INTO placement_item_attnum
          FROM pg_attribute
         WHERE attrelid = 'public.placement_items'::regclass
           AND attname = 'placement_item_id'
           AND NOT attisdropped;
    END IF;
    SELECT attnum INTO run_id_attnum
      FROM pg_attribute
     WHERE attrelid = 'public.v2_migration_runs'::regclass
       AND attname = 'run_id'
       AND NOT attisdropped;

    IF ingest_team_attnum IS NULL
       OR ingest_migration_attnum IS NULL
       OR marker_run_attnum IS NULL
       OR corpus_team_attnum IS NULL
       OR corpus_owner_attnum IS NULL
       OR corpus_ingest_attnum IS NULL
       OR corpus_placement_item_attnum IS NULL
       OR alias_team_attnum IS NULL
       OR alias_owner_attnum IS NULL
       OR (placement_items_exists AND (
           placement_team_attnum IS NULL
           OR placement_item_attnum IS NULL
       ))
       OR run_id_attnum IS NULL
    THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: expected lineage columns are missing';
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = 'knowledge_ingests_migration_run_id_fkey'
           AND contype = 'f'
           AND conrelid = 'public.knowledge_ingests'::regclass
           AND confrelid = 'public.v2_migration_runs'::regclass
           AND convalidated
           AND confdeltype = 'r'
           AND confupdtype = 'a'
           AND confmatchtype = 's'
           AND NOT condeferrable
           AND NOT condeferred
           AND conkey = ARRAY[ingest_migration_attnum]::smallint[]
           AND confkey = ARRAY[run_id_attnum]::smallint[]
    ) THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: knowledge ingest lineage FK definition is not the verified target';
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = 'v2_compatibility_markers_run_id_fkey'
           AND contype = 'f'
           AND conrelid = 'public.v2_compatibility_markers'::regclass
           AND confrelid = 'public.v2_migration_runs'::regclass
           AND convalidated
           AND confdeltype = 'r'
           AND confupdtype = 'a'
           AND confmatchtype = 's'
           AND NOT condeferrable
           AND NOT condeferred
           AND conkey = ARRAY[marker_run_attnum]::smallint[]
           AND confkey = ARRAY[run_id_attnum]::smallint[]
    ) THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: compatibility marker lineage FK definition is not the verified target';
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = 'v2_migration_corpus_items_team_id_fkey'
           AND contype = 'f'
           AND conrelid = 'public.v2_migration_corpus_items'::regclass
           AND confrelid = 'public.teams'::regclass
           AND convalidated
           AND confdeltype = 'r'
           AND confupdtype = 'a'
           AND confmatchtype = 's'
           AND NOT condeferrable
           AND NOT condeferred
           AND conkey = ARRAY[corpus_team_attnum]::smallint[]
           AND confkey = ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid = 'public.teams'::regclass AND attname = 'id' AND NOT attisdropped)]::smallint[]
    ) THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: corpus team FK definition is not the verified target';
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = 'v2_migration_corpus_items_team_id_owner_profile_id_fkey'
           AND contype = 'f'
           AND conrelid = 'public.v2_migration_corpus_items'::regclass
           AND confrelid = 'public.ownership_aliases'::regclass
           AND convalidated
           AND confdeltype = 'r'
           AND confupdtype = 'a'
           AND confmatchtype = 's'
           AND NOT condeferrable
           AND NOT condeferred
           AND conkey = ARRAY[corpus_team_attnum, corpus_owner_attnum]::smallint[]
           AND confkey = ARRAY[alias_team_attnum, alias_owner_attnum]::smallint[]
    ) THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: corpus ownership FK definition is not the verified target';
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = 'v2_migration_corpus_items_team_id_ingest_id_fkey'
           AND contype = 'f'
           AND conrelid = 'public.v2_migration_corpus_items'::regclass
           AND confrelid = 'public.knowledge_ingests'::regclass
           AND convalidated
           AND confdeltype = 'r'
           AND confupdtype = 'a'
           AND confmatchtype = 's'
           AND NOT condeferrable
           AND NOT condeferred
           AND conkey = ARRAY[corpus_team_attnum, corpus_ingest_attnum]::smallint[]
           AND confkey = ARRAY[ingest_team_attnum, (SELECT attnum FROM pg_attribute WHERE attrelid = 'public.knowledge_ingests'::regclass AND attname = 'ingest_id' AND NOT attisdropped)]::smallint[]
    ) THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: corpus ingest FK definition is not the verified target';
    END IF;

    IF placement_items_exists AND NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = 'v2_migration_corpus_items_team_id_placement_item_id_fkey'
           AND contype = 'f'
           AND conrelid = 'public.v2_migration_corpus_items'::regclass
           AND confrelid = to_regclass('public.placement_items')
           AND convalidated
           AND confdeltype = 'r'
           AND confupdtype = 'a'
           AND confmatchtype = 's'
           AND NOT condeferrable
           AND NOT condeferred
           AND conkey = ARRAY[corpus_team_attnum, corpus_placement_item_attnum]::smallint[]
           AND confkey = ARRAY[placement_team_attnum, placement_item_attnum]::smallint[]
    ) THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: corpus placement FK definition is not the verified target';
    END IF;

    SELECT opclass.oid
      INTO uuid_btree_opclass
      FROM pg_opclass AS opclass
      JOIN pg_am AS access_method ON access_method.oid = opclass.opcmethod
     WHERE access_method.amname = 'btree'
       AND opclass.opcnamespace = 'pg_catalog'::regnamespace
       AND opclass.opcname = 'uuid_ops'
       AND opclass.opcintype = 'uuid'::regtype
       AND opclass.opcdefault;
    IF uuid_btree_opclass IS NULL THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: default UUID btree operator class is missing';
    END IF;

    SELECT count(*) INTO index_count
      FROM pg_class AS index_class
      JOIN pg_namespace AS index_namespace ON index_namespace.oid = index_class.relnamespace
      JOIN pg_index AS index_row ON index_row.indexrelid = index_class.oid
      JOIN pg_am AS access_method ON access_method.oid = index_class.relam
     WHERE index_class.relname = 'knowledge_ingests_migration_run_idx'
       AND index_namespace.nspname = 'public'
       AND index_row.indrelid = 'public.knowledge_ingests'::regclass
       AND access_method.amname = 'btree'
       AND index_class.relpersistence = 'p'
       AND NOT index_row.indisunique
       AND NOT index_row.indisprimary
       AND NOT index_row.indisexclusion
       AND index_row.indimmediate
       AND index_row.indisvalid
       AND index_row.indisready
       AND index_row.indislive
       AND index_row.indnatts = 2
       AND index_row.indnkeyatts = 2
       AND index_row.indkey = format('%s %s', ingest_team_attnum, ingest_migration_attnum)::int2vector
       AND index_row.indcollation = '0 0'::oidvector
       AND index_row.indclass = format('%s %s', uuid_btree_opclass, uuid_btree_opclass)::oidvector
       AND index_row.indoption = '0 0'::int2vector
       AND pg_get_expr(index_row.indpred, index_row.indrelid) = '(migration_run_id IS NOT NULL)';
    IF index_count <> 1 THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: expected one verified knowledge ingest lineage index, found %',
            index_count;
    END IF;

    -- A column drop can remove views, functions, triggers, policies, indexes,
    -- and constraints that are not visible from the migration SQL. Allow only
    -- the five named foreign keys and the one named lineage index.
    FOR dependency IN
        SELECT pg_describe_object(dependency_row.classid, dependency_row.objid, dependency_row.objsubid) AS description,
               dependency_row.classid,
               dependency_row.objid,
               dependency_row.objsubid
          FROM pg_depend AS dependency_row
         WHERE dependency_row.refclassid = 'pg_class'::regclass
           AND dependency_row.refobjid = 'public.knowledge_ingests'::regclass
           AND dependency_row.refobjsubid = ingest_migration_attnum
    LOOP
        IF dependency.classid = 'pg_constraint'::regclass
           AND EXISTS (
               SELECT 1
                 FROM pg_constraint
                WHERE oid = dependency.objid
                  AND conname = ANY(detached_constraints)
           )
        THEN
            CONTINUE;
        END IF;
        IF dependency.classid = 'pg_class'::regclass
           AND dependency.objid = 'public.knowledge_ingests_migration_run_idx'::regclass
        THEN
            CONTINUE;
        END IF;
        RAISE EXCEPTION
            'migration-control detachment blocked: unexpected dependency on knowledge_ingests.migration_run_id: %',
            dependency.description;
    END LOOP;

    -- Retained objects may not reference the migration-control tables or the
    -- lineage column through a view, function, trigger, policy, or index. The
    -- catalog dependency query below handles static SQL dependencies; these
    -- definitions cover dynamic SQL and trigger bodies as well.
    SELECT string_agg(reference_name, '; ' ORDER BY reference_name)
      INTO unexpected
      FROM (
          SELECT format('view %I.%I', schemaname, viewname) AS reference_name
            FROM pg_views
           WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
             AND definition ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))'
          UNION ALL
          SELECT format('function %s', routine.oid::regprocedure) AS reference_name
            FROM pg_proc AS routine
            JOIN pg_namespace AS routine_namespace ON routine_namespace.oid = routine.pronamespace
            JOIN pg_language AS routine_language ON routine_language.oid = routine.prolang
           WHERE routine_namespace.nspname NOT IN ('pg_catalog', 'information_schema')
             AND routine.prokind IN ('f', 'p', 'w')
             AND routine_language.lanname IN ('sql', 'plpgsql')
             AND pg_get_functiondef(routine.oid) ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))'
          UNION ALL
          SELECT format('trigger %I on %I.%I', trigger_row.tgname, trigger_namespace.nspname, trigger_table.relname) AS reference_name
            FROM pg_trigger AS trigger_row
            JOIN pg_class AS trigger_table ON trigger_table.oid = trigger_row.tgrelid
            JOIN pg_namespace AS trigger_namespace ON trigger_namespace.oid = trigger_table.relnamespace
           WHERE NOT trigger_row.tgisinternal
             AND trigger_namespace.nspname NOT IN ('pg_catalog', 'information_schema')
             AND trigger_table.relname <> ALL(retired_tables)
             AND pg_get_triggerdef(trigger_row.oid) ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))'
          UNION ALL
          SELECT format('policy %I on %I.%I', policy_row.policyname, policy_row.schemaname, policy_row.tablename) AS reference_name
            FROM pg_policies AS policy_row
           WHERE policy_row.schemaname NOT IN ('pg_catalog', 'information_schema')
             AND policy_row.tablename <> ALL(retired_tables)
             AND (coalesce(policy_row.qual, '') ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))'
               OR coalesce(policy_row.with_check, '') ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))')
          UNION ALL
          SELECT format('index %I on %I.%I', index_row.indexname, index_row.schemaname, index_row.tablename) AS reference_name
            FROM pg_indexes AS index_row
           WHERE index_row.schemaname NOT IN ('pg_catalog', 'information_schema')
             AND index_row.tablename <> ALL(retired_tables)
             AND NOT (index_row.tablename = 'knowledge_ingests' AND index_row.indexname = 'knowledge_ingests_migration_run_idx')
             AND index_row.indexdef ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))'
      ) AS reference_rows;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: unexpected retained object references: %',
            unexpected;
    END IF;

    -- Every foreign key crossing the retired-schema boundary must be one of
    -- the five required constraints and, when present, the verified
    -- placement_items constraint detached below. This covers both retained
    -- objects pointing at migration-control tables and migration-control
    -- objects pointing back into canonical tables.
    SELECT string_agg(
               format('constraint %I on %I.%I references %I.%I',
                      constraint_row.conname,
                      source_namespace.nspname,
                      source_table.relname,
                      target_namespace.nspname,
                      target_table.relname),
               '; ' ORDER BY constraint_row.conname
           )
      INTO unexpected
      FROM pg_constraint AS constraint_row
      JOIN pg_class AS source_table ON source_table.oid = constraint_row.conrelid
      JOIN pg_namespace AS source_namespace ON source_namespace.oid = source_table.relnamespace
      JOIN pg_class AS target_table ON target_table.oid = constraint_row.confrelid
      JOIN pg_namespace AS target_namespace ON target_namespace.oid = target_table.relnamespace
     WHERE constraint_row.contype = 'f'
       AND source_namespace.nspname = 'public'
       AND target_namespace.nspname = 'public'
       AND ((source_table.relname = ANY(retired_tables)) <> (target_table.relname = ANY(retired_tables)))
       AND NOT (
           (constraint_row.conname = 'knowledge_ingests_migration_run_id_fkey'
            AND constraint_row.conrelid = 'public.knowledge_ingests'::regclass
            AND constraint_row.confrelid = 'public.v2_migration_runs'::regclass)
           OR (constraint_row.conname = 'v2_compatibility_markers_run_id_fkey'
               AND constraint_row.conrelid = 'public.v2_compatibility_markers'::regclass
               AND constraint_row.confrelid = 'public.v2_migration_runs'::regclass)
           OR (constraint_row.conname = 'v2_migration_corpus_items_team_id_fkey'
               AND constraint_row.conrelid = 'public.v2_migration_corpus_items'::regclass
               AND constraint_row.confrelid = 'public.teams'::regclass)
           OR (constraint_row.conname = 'v2_migration_corpus_items_team_id_owner_profile_id_fkey'
               AND constraint_row.conrelid = 'public.v2_migration_corpus_items'::regclass
               AND constraint_row.confrelid = 'public.ownership_aliases'::regclass)
           OR (constraint_row.conname = 'v2_migration_corpus_items_team_id_ingest_id_fkey'
               AND constraint_row.conrelid = 'public.v2_migration_corpus_items'::regclass
               AND constraint_row.confrelid = 'public.knowledge_ingests'::regclass)
           OR (constraint_row.conname = 'v2_migration_corpus_items_team_id_placement_item_id_fkey'
               AND constraint_row.conrelid = 'public.v2_migration_corpus_items'::regclass
               AND target_table.relname = 'placement_items')
       );
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: unexpected cross-boundary foreign keys: %',
            unexpected;
    END IF;

    -- Reject normal pg_depend references from non-retired objects to a retired
    -- table. Table-owned indexes, constraints, triggers, and policies are the
    -- retained control schema itself and are intentionally allowed.
    SELECT string_agg(dependency_item.description, '; ' ORDER BY dependency_item.description)
      INTO unexpected
      FROM (
          SELECT pg_describe_object(dependency_row.classid, dependency_row.objid, dependency_row.objsubid) AS description
            FROM pg_depend AS dependency_row
            JOIN pg_class AS target_table
              ON target_table.oid = dependency_row.refobjid
             AND target_table.relnamespace = 'public'::regnamespace
             AND target_table.relname = ANY(retired_tables)
           WHERE dependency_row.refclassid = 'pg_class'::regclass
             AND dependency_row.deptype = 'n'
             AND NOT (
                 dependency_row.classid = 'pg_class'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_index AS owned_index
                      WHERE owned_index.indexrelid = dependency_row.objid
                        AND owned_index.indrelid = target_table.oid
                 )
             )
             AND NOT (
                 dependency_row.classid = 'pg_constraint'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_constraint AS owned_constraint
                      WHERE owned_constraint.oid = dependency_row.objid
                        AND (
                            owned_constraint.conrelid = target_table.oid
                            OR (
                                owned_constraint.conrelid <> 0
                                AND EXISTS (
                                    SELECT 1
                                      FROM pg_class AS owning_table
                                     WHERE owning_table.oid = owned_constraint.conrelid
                                       AND owning_table.relnamespace = 'public'::regnamespace
                                       AND owning_table.relname = ANY(retired_tables)
                                )
                            )
                        )
                 )
             )
             AND NOT (
                 dependency_row.classid = 'pg_trigger'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_trigger AS owned_trigger
                      WHERE owned_trigger.oid = dependency_row.objid
                        AND owned_trigger.tgrelid = target_table.oid
                 )
             )
             AND NOT (
                 dependency_row.classid = 'pg_constraint'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_constraint AS detached_constraint
                      WHERE detached_constraint.oid = dependency_row.objid
                        AND detached_constraint.conname = ANY(detached_constraints)
                 )
             )
      ) AS dependency_item;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control detachment blocked: unexpected dependency on retired tables: %',
            unexpected;
    END IF;

END
$$;

ALTER TABLE public.knowledge_ingests
    DROP CONSTRAINT knowledge_ingests_migration_run_id_fkey;
DROP INDEX public.knowledge_ingests_migration_run_idx;
ALTER TABLE public.knowledge_ingests
    DROP COLUMN migration_run_id;

ALTER TABLE public.v2_compatibility_markers
    DROP CONSTRAINT v2_compatibility_markers_run_id_fkey;

ALTER TABLE public.v2_migration_corpus_items
    DROP CONSTRAINT v2_migration_corpus_items_team_id_fkey,
    DROP CONSTRAINT v2_migration_corpus_items_team_id_owner_profile_id_fkey,
    DROP CONSTRAINT v2_migration_corpus_items_team_id_ingest_id_fkey,
    DROP CONSTRAINT IF EXISTS v2_migration_corpus_items_team_id_placement_item_id_fkey;

DO $$
DECLARE
    retired_tables CONSTANT text[] := ARRAY[
        'v2_migration_runs',
        'v2_migration_corpus_items',
        'v2_migration_source_maps',
        'v2_migration_checkpoints',
        'v2_migration_errors',
        'v2_migration_exclusions',
        'v2_migration_gate_results',
        'v2_migration_operator_actions'
    ];
    remaining bigint;
BEGIN
    SELECT count(*)
      INTO remaining
      FROM pg_constraint AS constraint_row
      JOIN pg_class AS source_table ON source_table.oid = constraint_row.conrelid
      JOIN pg_namespace AS source_namespace ON source_namespace.oid = source_table.relnamespace
      JOIN pg_class AS target_table ON target_table.oid = constraint_row.confrelid
      JOIN pg_namespace AS target_namespace ON target_namespace.oid = target_table.relnamespace
     WHERE constraint_row.contype = 'f'
       AND source_namespace.nspname = 'public'
       AND target_namespace.nspname = 'public'
       AND ((source_table.relname = ANY(retired_tables)) <> (target_table.relname = ANY(retired_tables)));
    IF remaining <> 0 THEN
        RAISE EXCEPTION
            'migration-control detachment failed: cross-boundary foreign keys remain (%)',
            remaining;
    END IF;
END
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION
        '20260913010001 is irreversible: restore the verified pre-detachment schema and data backup or use a new ordered recovery migration; do not retry this Down operation';
END
$$;

-- +goose StatementEnd
