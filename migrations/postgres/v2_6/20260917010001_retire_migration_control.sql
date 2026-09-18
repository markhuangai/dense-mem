-- Lock/rewrite impact: the migration takes bounded ACCESS EXCLUSIVE locks on
-- the retired tables and the compatibility marker table, validates the catalog,
-- and drops only the eight retired table heaps. No retained table is rewritten.
-- RLS impact: migration mode is used for catalog inspection and DDL. Existing
-- application policies and transaction-local team/profile context are unchanged.
-- Backfill: none. The migration permanently removes completed migration input.
-- Backward compatibility: the detached release has no runtime dependency on
-- these tables; compatibility markers and all canonical state remain intact.
-- Rollback: irreversible after commit. Recovery requires a verified backup
-- restoration or a separately reviewed forward recovery migration.
-- Execution: the latest migration-control run must contain the explicit
-- maintenance authorization and every coordinated-stop/recovery gate before
-- this startup migration is allowed to perform its destructive DDL.

-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '30s';
SET LOCAL quote_all_identifiers = off;

LOCK TABLE public.v2_compatibility_markers,
           public.v2_migration_operator_actions,
           public.v2_migration_gate_results,
           public.v2_migration_exclusions,
           public.v2_migration_errors,
           public.v2_migration_checkpoints,
           public.v2_migration_source_maps,
           public.v2_migration_corpus_items,
           public.v2_migration_runs
    IN ACCESS EXCLUSIVE MODE;

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
    table_count bigint;
    marker_id uuid;
    marker_version text;
    marker_status text;
    retirement_run_id uuid;
    retirement_state text;
    retirement_preflight_approved boolean;
    retirement_backup_reference text;
    retirement_preflight_checks jsonb;
    retirement_operator_metadata jsonb;
    detached_release_evidence_ref text;
    current_main_evidence_ref text;
    backup_restore_evidence_ref text;
    coordinated_stop_evidence_ref text;
    catalog_preflight_evidence_ref text;
    expected_count bigint;
    current_count bigint;
    table_name text;
    unexpected text;
BEGIN
    SELECT count(*)
      INTO table_count
      FROM pg_class AS table_row
      JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
     WHERE namespace_row.nspname = 'public'
       AND table_row.relname = ANY(retired_tables)
       AND table_row.relkind = 'r';
    IF table_count <> cardinality(retired_tables) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: expected % regular tables, found %',
            cardinality(retired_tables), table_count;
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM public.goose_db_version
         WHERE version_id = 20260913010001
           AND is_applied
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: detached release 20260913010001 is not applied';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM pg_attribute AS attribute_row
          JOIN pg_class AS table_row ON table_row.oid = attribute_row.attrelid
          JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
         WHERE namespace_row.nspname = 'public'
           AND table_row.relname = 'knowledge_ingests'
           AND table_row.relkind = 'r'
           AND attribute_row.attname = 'migration_run_id'
           AND attribute_row.attnum > 0
           AND NOT attribute_row.attisdropped
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: knowledge_ingests.migration_run_id is still present';
    END IF;
    IF to_regclass('public.knowledge_ingests_migration_run_idx') IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: knowledge ingest lineage index is still present';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = ANY(detached_constraints)
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: detached foreign-key constraints remain';
    END IF;

    -- The detached marker policy is system-select only. Use that existing
    -- administrative read path for the marker gate, then restore migration
    -- mode before the remaining preflight and destructive DDL.
    PERFORM set_config('app.tx_mode', 'system', true);
    SELECT marker_row.marker_id,
           marker_row.version,
           marker_row.status
      INTO marker_id, marker_version, marker_status
      FROM public.v2_compatibility_markers AS marker_row
     WHERE marker_row.marker_kind = 'v2_cutover'
     ORDER BY marker_row.created_at DESC, marker_row.marker_id DESC
     LIMIT 1;
    IF NOT FOUND
       OR marker_version IS DISTINCT FROM 'dense-mem.v2.6.1.cutover.v1'
       OR marker_status IS DISTINCT FROM 'compatible'
    THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: latest compatible v2.6.1 cutover marker is required';
    END IF;
    SELECT run_row.run_id,
           run_row.state,
           run_row.preflight_approved,
           run_row.backup_reference,
           run_row.preflight_checks
      INTO retirement_run_id,
           retirement_state,
           retirement_preflight_approved,
           retirement_backup_reference,
           retirement_preflight_checks
      FROM public.v2_migration_runs AS run_row
     ORDER BY run_row.updated_at DESC, run_row.run_id DESC
     LIMIT 1;
    IF NOT FOUND
       OR retirement_state NOT IN ('ready_to_cutover', 'cut_over')
       OR retirement_preflight_approved IS DISTINCT FROM true
       OR btrim(retirement_backup_reference) = ''
       OR (retirement_preflight_checks @> $preflight$
           {
             "detached_release_deployed": true,
             "current_main_rehearsal": true,
             "backup_restore_rehearsal": true,
             "coordinated_stop": true,
             "catalog_preflight": true
           }
           $preflight$::jsonb) IS NOT TRUE
    THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: latest migration run lacks approved detached-release retirement preflight';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM (VALUES
              ('detached_release_deployed'),
              ('current_main_rehearsal'),
              ('backup_restore_rehearsal'),
              ('coordinated_stop'),
              ('catalog_preflight')
          ) AS required_gate(gate_name)
         WHERE NOT EXISTS (
             SELECT 1
               FROM public.v2_migration_gate_results AS gate_row
              WHERE gate_row.run_id = retirement_run_id
                AND gate_row.gate_name = required_gate.gate_name
                AND gate_row.outcome = 'pass'
                AND btrim(gate_row.evidence_ref) <> ''
                AND btrim(gate_row.evidence_hash) <> ''
         )
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: required operational gate evidence is incomplete';
    END IF;

    SELECT max(gate_row.evidence_ref) FILTER (WHERE gate_row.gate_name = 'detached_release_deployed'),
           max(gate_row.evidence_ref) FILTER (WHERE gate_row.gate_name = 'current_main_rehearsal'),
           max(gate_row.evidence_ref) FILTER (WHERE gate_row.gate_name = 'backup_restore_rehearsal'),
           max(gate_row.evidence_ref) FILTER (WHERE gate_row.gate_name = 'coordinated_stop'),
           max(gate_row.evidence_ref) FILTER (WHERE gate_row.gate_name = 'catalog_preflight')
      INTO detached_release_evidence_ref,
           current_main_evidence_ref,
           backup_restore_evidence_ref,
           coordinated_stop_evidence_ref,
           catalog_preflight_evidence_ref
      FROM public.v2_migration_gate_results AS gate_row
     WHERE gate_row.run_id = retirement_run_id
       AND gate_row.outcome = 'pass';

    SELECT action_row.metadata
      INTO retirement_operator_metadata
      FROM public.v2_migration_operator_actions AS action_row
     WHERE action_row.run_id = retirement_run_id
       AND action_row.action = 'retire_migration_control'
       AND btrim(action_row.actor) <> ''
       AND btrim(action_row.reason) <> ''
     ORDER BY action_row.created_at DESC, action_row.action_id DESC
     LIMIT 1;
    IF NOT FOUND
       OR jsonb_typeof(retirement_operator_metadata) IS DISTINCT FROM 'object'
       OR NULLIF(btrim(retirement_operator_metadata->>'approved_commit'), '') IS NULL
       OR btrim(retirement_operator_metadata->>'detached_release_receipt') IS DISTINCT FROM btrim(detached_release_evidence_ref)
       OR btrim(retirement_operator_metadata->>'current_main_rehearsal') IS DISTINCT FROM btrim(current_main_evidence_ref)
       OR btrim(retirement_operator_metadata->>'backup_restore_rehearsal') IS DISTINCT FROM btrim(backup_restore_evidence_ref)
       OR btrim(retirement_operator_metadata->>'coordinated_stop') IS DISTINCT FROM btrim(coordinated_stop_evidence_ref)
       OR btrim(retirement_operator_metadata->>'node_fence') IS DISTINCT FROM btrim(coordinated_stop_evidence_ref)
       OR btrim(retirement_operator_metadata->>'catalog_preflight') IS DISTINCT FROM btrim(catalog_preflight_evidence_ref)
    THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: explicit operator authorization is incomplete or not bound to gate evidence';
    END IF;

    IF jsonb_typeof(retirement_preflight_checks->'row_counts') IS DISTINCT FROM 'object'
       OR (
           SELECT count(*)
             FROM jsonb_object_keys(retirement_preflight_checks->'row_counts')
       ) <> cardinality(retired_tables)
       OR EXISTS (
           SELECT 1
             FROM unnest(retired_tables) AS required_table(table_name)
            WHERE (retirement_preflight_checks->'row_counts'->required_table.table_name) IS NULL
               OR (retirement_preflight_checks->'row_counts'->>required_table.table_name) !~ '^[0-9]+$'
       )
    THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: approved row-count snapshot is incomplete';
    END IF;

    FOREACH table_name IN ARRAY retired_tables LOOP
        expected_count := (retirement_preflight_checks->'row_counts'->>table_name)::bigint;
        EXECUTE format('SELECT count(*) FROM public.%I', table_name) INTO current_count;
        IF current_count IS DISTINCT FROM expected_count THEN
            RAISE EXCEPTION
                'migration-control retirement blocked: approved row-count snapshot differs for % (expected %, found %)',
                table_name, expected_count, current_count;
        END IF;
    END LOOP;
    PERFORM set_config('app.tx_mode', 'migration', true);

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
       AND ((source_table.relname = ANY(retired_tables)) <> (target_table.relname = ANY(retired_tables)));
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: unexpected cross-boundary foreign keys: %',
            unexpected;
    END IF;

    -- The detached tables still contain their intrinsic run_id foreign keys.
    -- Validate the exact table-owned inventory before allowing those children
    -- to be removed ahead of v2_migration_runs.
    SELECT string_agg(problem, '; ' ORDER BY problem)
      INTO unexpected
      FROM (
          WITH expected_columns(table_name, column_name, ordinal, type_name, not_null, default_expr) AS (
              VALUES
                  ('v2_migration_runs', 'run_id', 1, 'uuid', true, 'gen_random_uuid()'),
                  ('v2_migration_runs', 'migration_contract_version', 2, 'text', true, ''),
                  ('v2_migration_runs', 'corpus_version', 3, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'source_kind', 4, 'text', true, $expected$'neo4j'::text$expected$),
                  ('v2_migration_runs', 'state', 5, 'text', true, ''),
                  ('v2_migration_runs', 'phase', 6, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'required', 7, 'boolean', true, 'true'),
                  ('v2_migration_runs', 'preflight_approved', 8, 'boolean', true, 'false'),
                  ('v2_migration_runs', 'backup_reference', 9, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'preflight_checks', 10, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_runs', 'corpus_watermark', 11, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'corpus_hash', 12, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'total_items', 13, 'integer', true, '0'),
                  ('v2_migration_runs', 'completed_items', 14, 'integer', true, '0'),
                  ('v2_migration_runs', 'failed_items', 15, 'integer', true, '0'),
                  ('v2_migration_runs', 'excluded_items', 16, 'integer', true, '0'),
                  ('v2_migration_runs', 'last_error', 17, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'retryable', 18, 'boolean', true, 'true'),
                  ('v2_migration_runs', 'lease_owner', 19, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'checkpoint_key', 20, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_runs', 'checkpoint_value', 21, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_runs', 'started_at', 22, 'timestamp with time zone', false, ''),
                  ('v2_migration_runs', 'completed_at', 23, 'timestamp with time zone', false, ''),
                  ('v2_migration_runs', 'cutover_at', 24, 'timestamp with time zone', false, ''),
                  ('v2_migration_runs', 'created_at', 25, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_runs', 'updated_at', 26, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_runs', 'claim_epoch', 27, 'integer', true, '1'),
                  ('v2_migration_corpus_items', 'item_id', 1, 'uuid', true, 'gen_random_uuid()'),
                  ('v2_migration_corpus_items', 'run_id', 2, 'uuid', true, ''),
                  ('v2_migration_corpus_items', 'team_id', 3, 'uuid', true, ''),
                  ('v2_migration_corpus_items', 'owner_profile_id', 4, 'uuid', false, ''),
                  ('v2_migration_corpus_items', 'source_kind', 5, 'text', true, $expected$'neo4j'::text$expected$),
                  ('v2_migration_corpus_items', 'source_id', 6, 'text', true, ''),
                  ('v2_migration_corpus_items', 'source_hash', 7, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_corpus_items', 'item_kind', 8, 'text', true, ''),
                  ('v2_migration_corpus_items', 'outcome', 9, 'text', true, $expected$'pending'::text$expected$),
                  ('v2_migration_corpus_items', 'ingest_id', 10, 'uuid', false, ''),
                  ('v2_migration_corpus_items', 'placement_item_id', 11, 'uuid', false, ''),
                  ('v2_migration_corpus_items', 'exclusion_reason', 12, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_corpus_items', 'metadata', 13, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_corpus_items', 'created_at', 14, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_corpus_items', 'updated_at', 15, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_source_maps', 'map_id', 1, 'uuid', true, 'gen_random_uuid()'),
                  ('v2_migration_source_maps', 'run_id', 2, 'uuid', true, ''),
                  ('v2_migration_source_maps', 'source_kind', 3, 'text', true, $expected$'neo4j'::text$expected$),
                  ('v2_migration_source_maps', 'source_id', 4, 'text', true, ''),
                  ('v2_migration_source_maps', 'target_type', 5, 'text', true, ''),
                  ('v2_migration_source_maps', 'target_id', 6, 'text', true, ''),
                  ('v2_migration_source_maps', 'metadata', 7, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_source_maps', 'created_at', 8, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_checkpoints', 'run_id', 1, 'uuid', true, ''),
                  ('v2_migration_checkpoints', 'checkpoint_key', 2, 'text', true, ''),
                  ('v2_migration_checkpoints', 'checkpoint_value', 3, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_checkpoints', 'lease_owner', 4, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_checkpoints', 'updated_at', 5, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_errors', 'error_id', 1, 'uuid', true, 'gen_random_uuid()'),
                  ('v2_migration_errors', 'run_id', 2, 'uuid', true, ''),
                  ('v2_migration_errors', 'source_kind', 3, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_errors', 'source_id', 4, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_errors', 'phase', 5, 'text', true, ''),
                  ('v2_migration_errors', 'error_code', 6, 'text', true, ''),
                  ('v2_migration_errors', 'message', 7, 'text', true, ''),
                  ('v2_migration_errors', 'retryable', 8, 'boolean', true, 'true'),
                  ('v2_migration_errors', 'metadata', 9, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_errors', 'created_at', 10, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_exclusions', 'exclusion_id', 1, 'uuid', true, 'gen_random_uuid()'),
                  ('v2_migration_exclusions', 'run_id', 2, 'uuid', true, ''),
                  ('v2_migration_exclusions', 'source_kind', 3, 'text', true, $expected$'neo4j'::text$expected$),
                  ('v2_migration_exclusions', 'source_id', 4, 'text', true, ''),
                  ('v2_migration_exclusions', 'reason', 5, 'text', true, ''),
                  ('v2_migration_exclusions', 'blocks_cutover', 6, 'boolean', true, 'true'),
                  ('v2_migration_exclusions', 'metadata', 7, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_exclusions', 'created_at', 8, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_gate_results', 'gate_id', 1, 'uuid', true, 'gen_random_uuid()'),
                  ('v2_migration_gate_results', 'run_id', 2, 'uuid', true, ''),
                  ('v2_migration_gate_results', 'gate_name', 3, 'text', true, ''),
                  ('v2_migration_gate_results', 'outcome', 4, 'text', true, ''),
                  ('v2_migration_gate_results', 'evidence_ref', 5, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_gate_results', 'evidence_hash', 6, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_gate_results', 'message', 7, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_gate_results', 'metadata', 8, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_gate_results', 'created_at', 9, 'timestamp with time zone', true, 'now()'),
                  ('v2_migration_operator_actions', 'action_id', 1, 'uuid', true, 'gen_random_uuid()'),
                  ('v2_migration_operator_actions', 'run_id', 2, 'uuid', false, ''),
                  ('v2_migration_operator_actions', 'action', 3, 'text', true, ''),
                  ('v2_migration_operator_actions', 'actor', 4, 'text', true, ''),
                  ('v2_migration_operator_actions', 'remote_ip', 5, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_operator_actions', 'reason', 6, 'text', true, $expected$''::text$expected$),
                  ('v2_migration_operator_actions', 'metadata', 7, 'jsonb', true, $expected$'{}'::jsonb$expected$),
                  ('v2_migration_operator_actions', 'created_at', 8, 'timestamp with time zone', true, 'now()')
          ),
          actual_columns AS (
              SELECT table_row.relname AS table_name,
                     attribute_row.attname AS column_name,
                     attribute_row.attnum AS ordinal,
                     format_type(attribute_row.atttypid, attribute_row.atttypmod) AS type_name,
                     attribute_row.attnotnull AS not_null,
                     regexp_replace(lower(coalesce(pg_get_expr(default_row.adbin, default_row.adrelid), '')), '[[:space:]]+', '', 'g') AS default_expr
                FROM pg_class AS table_row
                JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
                JOIN pg_attribute AS attribute_row ON attribute_row.attrelid = table_row.oid
                LEFT JOIN pg_attrdef AS default_row
                  ON default_row.adrelid = attribute_row.attrelid
                 AND default_row.adnum = attribute_row.attnum
               WHERE namespace_row.nspname = 'public'
                 AND table_row.relname = ANY(retired_tables)
                 AND attribute_row.attnum > 0
                 AND NOT attribute_row.attisdropped
          )
          SELECT format('%s.%s: expected column definition', expected.table_name, expected.column_name) AS problem
            FROM expected_columns AS expected
            LEFT JOIN actual_columns AS actual
              ON actual.table_name = expected.table_name
             AND actual.column_name = expected.column_name
           WHERE actual.column_name IS NULL
              OR actual.ordinal <> expected.ordinal
              OR actual.type_name <> expected.type_name
              OR actual.not_null <> expected.not_null
              OR actual.default_expr <> expected.default_expr
          UNION ALL
          SELECT format('%s.%s: unexpected column', actual.table_name, actual.column_name)
            FROM actual_columns AS actual
            LEFT JOIN expected_columns AS expected
              ON expected.table_name = actual.table_name
             AND expected.column_name = actual.column_name
           WHERE expected.column_name IS NULL
      ) AS column_problems;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target column inventory mismatch: %',
            unexpected;
    END IF;

    SELECT string_agg(problem, '; ' ORDER BY problem)
      INTO unexpected
      FROM (
          WITH expected_counts(table_name, constraint_count, index_count, policy_count) AS (
              VALUES
                  ('v2_migration_runs', 7, 3, 1),
                  ('v2_migration_corpus_items', 5, 5, 1),
                  ('v2_migration_source_maps', 4, 2, 1),
                  ('v2_migration_checkpoints', 3, 1, 1),
                  ('v2_migration_errors', 3, 2, 1),
                  ('v2_migration_exclusions', 4, 2, 1),
                  ('v2_migration_gate_results', 5, 2, 1),
                  ('v2_migration_operator_actions', 3, 2, 1)
          ),
          actual_counts AS (
              SELECT table_row.relname AS table_name,
                     -- PostgreSQL 18 exposes NOT NULL declarations as
                     -- pg_constraint rows with contype = 'n'. Nullability is
                     -- already validated with the column inventory above;
                     -- keep this owned-constraint count portable across
                     -- PostgreSQL versions by counting semantic constraints.
                     (SELECT count(*)
                        FROM pg_constraint AS constraint_row
                       WHERE constraint_row.conrelid = table_row.oid
                         AND constraint_row.contype <> 'n') AS constraint_count,
                     (SELECT count(*) FROM pg_index AS index_row WHERE index_row.indrelid = table_row.oid) AS index_count,
                     (SELECT count(*) FROM pg_policy AS policy_row WHERE policy_row.polrelid = table_row.oid) AS policy_count
                FROM pg_class AS table_row
                JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
               WHERE namespace_row.nspname = 'public'
                 AND table_row.relname = ANY(retired_tables)
          )
          SELECT format('%s: expected owned object counts constraints=%s indexes=%s policies=%s, found constraints=%s indexes=%s policies=%s',
                        expected.table_name, expected.constraint_count, expected.index_count, expected.policy_count,
                        actual.constraint_count, actual.index_count, actual.policy_count) AS problem
            FROM expected_counts AS expected
            JOIN actual_counts AS actual USING (table_name)
           WHERE expected.constraint_count <> actual.constraint_count
              OR expected.index_count <> actual.index_count
              OR expected.policy_count <> actual.policy_count
          UNION ALL
          SELECT format('sequence owned by %I', target_table.relname)
            FROM pg_depend AS dependency_row
            JOIN pg_class AS sequence_row ON sequence_row.oid = dependency_row.objid
            JOIN pg_class AS target_table ON target_table.oid = dependency_row.refobjid
            JOIN pg_namespace AS namespace_row ON namespace_row.oid = target_table.relnamespace
           WHERE dependency_row.classid = 'pg_class'::regclass
             AND dependency_row.refclassid = 'pg_class'::regclass
             AND dependency_row.deptype = 'a'
             AND sequence_row.relkind = 'S'
             AND namespace_row.nspname = 'public'
             AND target_table.relname = ANY(retired_tables)
      ) AS object_problems;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target catalog inventory mismatch: %',
            unexpected;
    END IF;

    IF EXISTS (
        WITH expected_kinds(table_name, constraint_kind, expected_count) AS (
            VALUES
                ('v2_migration_runs', 'p', 1), ('v2_migration_runs', 'c', 6),
                ('v2_migration_runs', 'f', 0), ('v2_migration_runs', 'u', 0),
                ('v2_migration_corpus_items', 'p', 1), ('v2_migration_corpus_items', 'c', 2),
                ('v2_migration_corpus_items', 'f', 1), ('v2_migration_corpus_items', 'u', 1),
                ('v2_migration_source_maps', 'p', 1), ('v2_migration_source_maps', 'c', 1),
                ('v2_migration_source_maps', 'f', 1), ('v2_migration_source_maps', 'u', 1),
                ('v2_migration_checkpoints', 'p', 1), ('v2_migration_checkpoints', 'c', 1),
                ('v2_migration_checkpoints', 'f', 1),
                ('v2_migration_errors', 'p', 1), ('v2_migration_errors', 'c', 1),
                ('v2_migration_errors', 'f', 1),
                ('v2_migration_exclusions', 'p', 1), ('v2_migration_exclusions', 'c', 1),
                ('v2_migration_exclusions', 'f', 1), ('v2_migration_exclusions', 'u', 1),
                ('v2_migration_gate_results', 'p', 1), ('v2_migration_gate_results', 'c', 2),
                ('v2_migration_gate_results', 'f', 1), ('v2_migration_gate_results', 'u', 1),
                ('v2_migration_operator_actions', 'p', 1), ('v2_migration_operator_actions', 'c', 1),
                ('v2_migration_operator_actions', 'f', 1)
        )
        SELECT 1
          FROM expected_kinds AS expected
         WHERE expected.expected_count <> (
             SELECT count(*)
               FROM pg_constraint AS constraint_row
               JOIN pg_class AS table_row ON table_row.oid = constraint_row.conrelid
               JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
              WHERE namespace_row.nspname = 'public'
                AND table_row.relname = expected.table_name
                AND constraint_row.contype = expected.constraint_kind
         )
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target constraint kind inventory mismatch';
    END IF;

    IF EXISTS (
        WITH expected_primary_keys(table_name, column_names) AS (
            VALUES
                ('v2_migration_runs', ARRAY['run_id']::text[]),
                ('v2_migration_corpus_items', ARRAY['item_id']::text[]),
                ('v2_migration_source_maps', ARRAY['map_id']::text[]),
                ('v2_migration_checkpoints', ARRAY['run_id', 'checkpoint_key']::text[]),
                ('v2_migration_errors', ARRAY['error_id']::text[]),
                ('v2_migration_exclusions', ARRAY['exclusion_id']::text[]),
                ('v2_migration_gate_results', ARRAY['gate_id']::text[]),
                ('v2_migration_operator_actions', ARRAY['action_id']::text[])
        )
        SELECT 1
          FROM expected_primary_keys AS expected
         WHERE NOT EXISTS (
             SELECT 1
               FROM pg_constraint AS constraint_row
               JOIN pg_class AS table_row ON table_row.oid = constraint_row.conrelid
               JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
              WHERE namespace_row.nspname = 'public'
                AND table_row.relname = expected.table_name
                AND constraint_row.contype = 'p'
                AND constraint_row.conkey = ARRAY(
                    SELECT attribute_row.attnum
                      FROM unnest(expected.column_names) WITH ORDINALITY AS expected_column(column_name, ordinal)
                      JOIN pg_attribute AS attribute_row
                        ON attribute_row.attrelid = table_row.oid
                       AND attribute_row.attname = expected_column.column_name
                       AND NOT attribute_row.attisdropped
                     ORDER BY expected_column.ordinal
                )::smallint[]
         )
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target primary-key inventory mismatch';
    END IF;

    IF EXISTS (
        WITH expected_foreign_keys(table_name, delete_action) AS (
            VALUES
                ('v2_migration_corpus_items', 'c'),
                ('v2_migration_source_maps', 'c'),
                ('v2_migration_checkpoints', 'c'),
                ('v2_migration_errors', 'c'),
                ('v2_migration_exclusions', 'c'),
                ('v2_migration_gate_results', 'c'),
                ('v2_migration_operator_actions', 'n')
        )
        SELECT 1
          FROM expected_foreign_keys AS expected
         WHERE NOT EXISTS (
             SELECT 1
               FROM pg_constraint AS constraint_row
               JOIN pg_class AS table_row ON table_row.oid = constraint_row.conrelid
               JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
              WHERE namespace_row.nspname = 'public'
                AND table_row.relname = expected.table_name
                AND constraint_row.contype = 'f'
                AND constraint_row.confrelid = 'public.v2_migration_runs'::regclass
                AND constraint_row.confdeltype = expected.delete_action
                AND constraint_row.conkey = ARRAY[
                    (SELECT attribute_row.attnum
                       FROM pg_attribute AS attribute_row
                      WHERE attribute_row.attrelid = table_row.oid
                        AND attribute_row.attname = 'run_id'
                        AND NOT attribute_row.attisdropped)
                ]::smallint[]
                AND constraint_row.confkey = ARRAY[
                    (SELECT attribute_row.attnum
                       FROM pg_attribute AS attribute_row
                      WHERE attribute_row.attrelid = constraint_row.confrelid
                        AND attribute_row.attname = 'run_id'
                        AND NOT attribute_row.attisdropped)
                ]::smallint[]
         )
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target foreign-key inventory mismatch';
    END IF;

    IF EXISTS (
        WITH expected_unique_keys(table_name, column_names) AS (
            VALUES
                ('v2_migration_corpus_items', ARRAY['run_id', 'source_kind', 'source_id']::text[]),
                ('v2_migration_source_maps', ARRAY['run_id', 'source_kind', 'source_id', 'target_type', 'target_id']::text[]),
                ('v2_migration_exclusions', ARRAY['run_id', 'source_kind', 'source_id']::text[]),
                ('v2_migration_gate_results', ARRAY['run_id', 'gate_name']::text[])
        )
        SELECT 1
          FROM expected_unique_keys AS expected
         WHERE NOT EXISTS (
             SELECT 1
               FROM pg_constraint AS constraint_row
               JOIN pg_class AS table_row ON table_row.oid = constraint_row.conrelid
               JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
              WHERE namespace_row.nspname = 'public'
                AND table_row.relname = expected.table_name
                AND constraint_row.contype = 'u'
                AND constraint_row.conkey = ARRAY(
                    SELECT attribute_row.attnum
                      FROM unnest(expected.column_names) WITH ORDINALITY AS expected_column(column_name, ordinal)
                      JOIN pg_attribute AS attribute_row
                        ON attribute_row.attrelid = table_row.oid
                       AND attribute_row.attname = expected_column.column_name
                       AND NOT attribute_row.attisdropped
                     ORDER BY expected_column.ordinal
                )::smallint[]
         )
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target unique-key inventory mismatch';
    END IF;

    SELECT string_agg(problem, '; ' ORDER BY problem)
      INTO unexpected
      FROM (
          WITH expected_checks(table_name, constraint_name, definition_pattern) AS (
              VALUES
                  ('v2_migration_runs', 'v2_migration_runs_state_check', $pattern$state.*required.*preflight.*ready.*paused_retryable.*verifying.*ready_to_cutover.*cut_over.*incompatible$pattern$),
                  ('v2_migration_runs', 'v2_migration_runs_json_check', $pattern$preflight_checks.*object.*checkpoint_value.*object$pattern$),
                  ('v2_migration_runs', 'v2_migration_runs_total_items_check', $pattern$total_items.*>=.*0$pattern$),
                  ('v2_migration_runs', 'v2_migration_runs_completed_items_check', $pattern$completed_items.*>=.*0$pattern$),
                  ('v2_migration_runs', 'v2_migration_runs_failed_items_check', $pattern$failed_items.*>=.*0$pattern$),
                  ('v2_migration_runs', 'v2_migration_runs_excluded_items_check', $pattern$excluded_items.*>=.*0$pattern$),
                  ('v2_migration_corpus_items', 'v2_migration_corpus_items_outcome_check', $pattern$outcome.*pending.*accepted.*needs_review.*rejected.*quarantined.*failed.*excluded$pattern$),
                  ('v2_migration_corpus_items', 'v2_migration_corpus_items_metadata_check', $pattern$jsonb_typeof.*metadata.*object$pattern$),
                  ('v2_migration_source_maps', 'v2_migration_source_maps_metadata_check', $pattern$jsonb_typeof.*metadata.*object$pattern$),
                  ('v2_migration_checkpoints', 'v2_migration_checkpoints_json_check', $pattern$jsonb_typeof.*checkpoint_value.*object$pattern$),
                  ('v2_migration_errors', 'v2_migration_errors_metadata_check', $pattern$jsonb_typeof.*metadata.*object$pattern$),
                  ('v2_migration_exclusions', 'v2_migration_exclusions_metadata_check', $pattern$jsonb_typeof.*metadata.*object$pattern$),
                  ('v2_migration_gate_results', 'v2_migration_gate_results_outcome_check', $pattern$outcome.*pass.*fail.*warning$pattern$),
                  ('v2_migration_gate_results', 'v2_migration_gate_results_metadata_check', $pattern$jsonb_typeof.*metadata.*object$pattern$),
                  ('v2_migration_operator_actions', 'v2_migration_operator_actions_metadata_check', $pattern$jsonb_typeof.*metadata.*object$pattern$)
          ),
          actual_checks AS (
              SELECT table_row.relname AS table_name,
                     constraint_row.conname AS constraint_name,
                     lower(pg_get_constraintdef(constraint_row.oid)) AS definition
                FROM pg_constraint AS constraint_row
                JOIN pg_class AS table_row ON table_row.oid = constraint_row.conrelid
                JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
               WHERE namespace_row.nspname = 'public'
                 AND table_row.relname = ANY(retired_tables)
                 AND constraint_row.contype = 'c'
          )
          SELECT format('%s.%s: expected check definition', expected.table_name, expected.constraint_name) AS problem
            FROM expected_checks AS expected
            LEFT JOIN actual_checks AS actual
              ON actual.table_name = expected.table_name
             AND actual.constraint_name = expected.constraint_name
           WHERE actual.constraint_name IS NULL
              OR actual.definition !~ expected.definition_pattern
      ) AS check_problems;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target check definition mismatch: %',
            unexpected;
    END IF;

    SELECT string_agg(problem, '; ' ORDER BY problem)
      INTO unexpected
      FROM (
          WITH expected_indexes(table_name, index_name, definition_pattern) AS (
              VALUES
                  ('v2_migration_runs', 'idx_v2_migration_runs_single_active', $pattern$state.*required.*preflight.*ready.*paused_retryable.*verifying.*ready_to_cutover$pattern$),
                  ('v2_migration_runs', 'idx_v2_migration_runs_state_updated', $pattern$state.*updated_at.*desc$pattern$),
                  ('v2_migration_corpus_items', 'v2_migration_corpus_run_ingest_idx', $pattern$run_id.*team_id.*ingest_id.*where.*ingest_id.*is not null$pattern$),
                  ('v2_migration_corpus_items', 'idx_v2_migration_corpus_run_outcome', $pattern$run_id.*outcome.*updated_at.*desc$pattern$),
                  ('v2_migration_corpus_items', 'idx_v2_migration_corpus_team_owner', $pattern$run_id.*team_id.*owner_profile_id.*outcome$pattern$),
                  ('v2_migration_errors', 'idx_v2_migration_errors_run_phase', $pattern$run_id.*phase.*created_at.*desc$pattern$),
                  ('v2_migration_operator_actions', 'idx_v2_migration_operator_actions_run_created', $pattern$run_id.*created_at.*desc$pattern$)
          ),
          actual_indexes AS (
              SELECT index_row.tablename AS table_name,
                     index_row.indexname AS index_name,
                     lower(index_row.indexdef) AS definition
                FROM pg_indexes AS index_row
               WHERE index_row.schemaname = 'public'
                 AND index_row.tablename = ANY(retired_tables)
          )
          SELECT format('%s.%s: expected index definition', expected.table_name, expected.index_name) AS problem
            FROM expected_indexes AS expected
            LEFT JOIN actual_indexes AS actual
              ON actual.table_name = expected.table_name
             AND actual.index_name = expected.index_name
           WHERE actual.index_name IS NULL
              OR actual.definition !~ expected.definition_pattern
      ) AS index_problems;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target index definition mismatch: %',
            unexpected;
    END IF;

    SELECT string_agg(problem, '; ' ORDER BY problem)
      INTO unexpected
      FROM (
          WITH expected_policies(table_name, policy_name) AS (
              VALUES
                  ('v2_migration_runs', 'v2_migration_runs_system_select'),
                  ('v2_migration_corpus_items', 'v2_migration_corpus_items_system_select'),
                  ('v2_migration_source_maps', 'v2_migration_source_maps_system_select'),
                  ('v2_migration_checkpoints', 'v2_migration_checkpoints_system_select'),
                  ('v2_migration_errors', 'v2_migration_errors_system_select'),
                  ('v2_migration_exclusions', 'v2_migration_exclusions_system_select'),
                  ('v2_migration_gate_results', 'v2_migration_gate_results_system_select'),
                  ('v2_migration_operator_actions', 'v2_migration_operator_actions_system_select')
          )
          SELECT format('%s.%s: expected policy definition', expected.table_name, expected.policy_name) AS problem
            FROM expected_policies AS expected
            LEFT JOIN pg_policies AS actual
              ON actual.schemaname = 'public'
             AND actual.tablename = expected.table_name
             AND actual.policyname = expected.policy_name
           WHERE actual.policyname IS NULL
              OR actual.cmd <> 'SELECT'
              OR lower(coalesce(actual.qual, '')) !~ $pattern$current_setting.*app\.tx_mode.*=.*system$pattern$
              OR actual.with_check IS NOT NULL
      ) AS policy_problems;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: target policy definition mismatch: %',
            unexpected;
    END IF;

    IF EXISTS (
        SELECT 1
          FROM pg_trigger AS trigger_row
          JOIN pg_class AS table_row ON table_row.oid = trigger_row.tgrelid
          JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
         WHERE namespace_row.nspname = 'public'
           AND table_row.relname = ANY(retired_tables)
           AND NOT trigger_row.tgisinternal
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: unexpected trigger owned by a retired table';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM pg_rewrite AS rewrite_row
          JOIN pg_class AS table_row ON table_row.oid = rewrite_row.ev_class
          JOIN pg_namespace AS namespace_row ON namespace_row.oid = table_row.relnamespace
         WHERE namespace_row.nspname = 'public'
           AND table_row.relname = ANY(retired_tables)
           AND rewrite_row.rulename <> '_RETURN'
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: unexpected rule owned by a retired table';
    END IF;

    IF EXISTS (
        SELECT 1
          FROM pg_inherits AS inheritance_row
          JOIN pg_class AS relation_row
            ON relation_row.oid IN (inheritance_row.inhrelid, inheritance_row.inhparent)
          JOIN pg_namespace AS namespace_row ON namespace_row.oid = relation_row.relnamespace
         WHERE namespace_row.nspname = 'public'
           AND relation_row.relname = ANY(retired_tables)
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: retired tables participate in inheritance';
    END IF;

    -- A table drop also removes indexes, constraints, policies, sequences,
    -- defaults, composite types, rules, and triggers owned by that table. Any
    -- other catalog dependency is an external object and must stop the drop.
    SELECT string_agg(dependency_result.description, '; ' ORDER BY dependency_result.description)
      INTO unexpected
      FROM (
          SELECT pg_describe_object(dependency_row.classid,
                                    dependency_row.objid,
                                    dependency_row.objsubid) AS description
            FROM pg_depend AS dependency_row
            JOIN pg_class AS target_table
              ON target_table.oid = dependency_row.refobjid
             AND target_table.relnamespace = 'public'::regnamespace
             AND target_table.relname = ANY(retired_tables)
           WHERE dependency_row.refclassid = 'pg_class'::regclass
             AND dependency_row.deptype <> 'i'
             AND NOT (
                 dependency_row.classid = 'pg_class'::regclass
                 AND (
                     EXISTS (
                         SELECT 1
                           FROM pg_index AS owned_index
                          WHERE owned_index.indexrelid = dependency_row.objid
                            AND owned_index.indrelid = target_table.oid
                     )
                     OR EXISTS (
                         SELECT 1
                           FROM pg_class AS sequence_row
                          WHERE sequence_row.oid = dependency_row.objid
                            AND sequence_row.relkind = 'S'
                            AND dependency_row.deptype = 'a'
                     )
                 )
             )
             AND NOT (
                 dependency_row.classid = 'pg_constraint'::regclass
                 AND EXISTS (
                 SELECT 1
                       FROM pg_constraint AS owned_constraint
                      WHERE owned_constraint.oid = dependency_row.objid
                        AND EXISTS (
                            SELECT 1
                              FROM pg_class AS owning_table
                             WHERE owning_table.oid = owned_constraint.conrelid
                               AND owning_table.relnamespace = 'public'::regnamespace
                               AND owning_table.relname = ANY(retired_tables)
                        )
             )
             )
             AND NOT (
                 dependency_row.classid = 'pg_policy'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_policy AS owned_policy
                      WHERE owned_policy.oid = dependency_row.objid
                        AND owned_policy.polrelid = target_table.oid
                 )
             )
             AND NOT (
                 dependency_row.classid = 'pg_attrdef'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_attrdef AS owned_default
                      WHERE owned_default.oid = dependency_row.objid
                        AND owned_default.adrelid = target_table.oid
                 )
             )
             AND NOT (
                 dependency_row.classid = 'pg_type'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_type AS owned_type
                      WHERE owned_type.oid = dependency_row.objid
                        AND owned_type.typrelid = target_table.oid
                 )
             )
             AND NOT (
                 dependency_row.classid = 'pg_rewrite'::regclass
                 AND EXISTS (
                     SELECT 1
                       FROM pg_rewrite AS owned_rule
                      WHERE owned_rule.oid = dependency_row.objid
                        AND owned_rule.ev_class = target_table.oid
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
      ) AS dependency_result;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: unexpected dependency on retired tables: %',
            unexpected;
    END IF;

    -- Catalog dependency tracking does not see dynamic SQL in definitions.
    -- Scan retained definitions as a second, bounded guard before mutation.
    SELECT string_agg(reference_name, '; ' ORDER BY reference_name)
      INTO unexpected
      FROM (
          SELECT format('view %I.%I', view_row.schemaname, view_row.viewname) AS reference_name
            FROM pg_views AS view_row
           WHERE view_row.schemaname NOT IN ('pg_catalog', 'information_schema')
             AND view_row.viewname <> ALL(retired_tables)
             AND view_row.definition ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))'
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
             AND index_row.indexdef ~* '(v2_migration_(runs|corpus_items|source_maps|checkpoints|errors|exclusions|gate_results|operator_actions)([^a-z_]|$)|migration_run_id([^a-z_]|$))'
      ) AS definition_rows;
    IF unexpected IS NOT NULL THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: unexpected retained definition references: %',
            unexpected;
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM public.goose_db_version
         WHERE version_id = 20260913020001
           AND is_applied
    ) THEN
        RAISE EXCEPTION
            'migration-control retirement blocked: required runtime migration 20260913020001 is not applied';
    END IF;
END
$$;

DROP TABLE public.v2_migration_operator_actions RESTRICT;
DROP TABLE public.v2_migration_gate_results RESTRICT;
DROP TABLE public.v2_migration_exclusions RESTRICT;
DROP TABLE public.v2_migration_errors RESTRICT;
DROP TABLE public.v2_migration_checkpoints RESTRICT;
DROP TABLE public.v2_migration_source_maps RESTRICT;
DROP TABLE public.v2_migration_corpus_items RESTRICT;
DROP TABLE public.v2_migration_runs RESTRICT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION
        '20260917010001 is irreversible: restore the verified pre-retirement backup and schema or use a new ordered recovery migration; do not recreate empty migration-control tables';
END
$$;

-- +goose StatementEnd
