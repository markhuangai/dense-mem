-- Lock/rewrite impact: the guarded transactional drop takes short table locks;
-- it does not rewrite search_documents or change any canonical rows.
-- RLS impact: migration mode is used only for catalog inspection; no row-level
-- visibility or application policy is changed.
-- Recovery: the exact CREATE INDEX statement is recorded below. Down refuses
-- automatic recreation because the target may have been intentionally absent.

-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '30s';

LOCK TABLE public.search_index_generations IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE public.search_documents IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    expected_contract CONSTANT uuid := '1a06f88d-e92e-4f3c-8d27-0b747bd8cf17';
    target_name CONSTANT text := 'v2_search_1a06f88de92e_3072_halfvec_hnsw_idx';
    active_name CONSTANT text := 'search_1a06f88de92e_3072_halfvec_hnsw_idx';
    target_oid oid;
    active_oid oid;
    target_generation_count integer;
    active_generation_count integer;
    target_generation integer;
    active_generation integer;
    target_contract uuid;
    active_contract uuid;
    target_dimensions integer;
    active_dimensions integer;
    target_strategy text;
    active_strategy text;
    target_operator_class text;
    active_operator_class text;
    target_expression text;
    active_expression text;
    target_state text;
    active_state text;
    target_activated_at timestamptz;
    active_activated_at timestamptz;
    active_contract_state text;
    physical record;
    physical_count integer := 0;
    incoming_dependency_count bigint;
    constraint_count bigint;
    inheritance_count bigint;
    normalized_definition text;
    expected_definition text;
BEGIN
    target_oid := to_regclass(format('public.%I', target_name));
    IF target_oid IS NULL THEN
        RETURN;
    END IF;

    active_oid := to_regclass(format('public.%I', active_name));
    IF active_oid IS NULL OR active_oid = target_oid THEN
        RAISE EXCEPTION
            'cannot retire deprecated ANN index %: the expected active replacement is missing',
            target_name;
    END IF;

    SELECT count(*)
      INTO target_generation_count
      FROM public.search_index_generations
     WHERE physical_index_name = target_name;
    IF target_generation_count <> 1 THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: expected one matching deprecated generation, found %',
            target_name, target_generation_count;
    END IF;

    SELECT generation, embedding_contract_id, embedding_dimensions,
           ann_strategy, operator_class, indexed_expression, activation_state,
           activated_at
      INTO target_generation, target_contract, target_dimensions,
           target_strategy, target_operator_class, target_expression,
           target_state, target_activated_at
      FROM public.search_index_generations
     WHERE physical_index_name = target_name;

    IF target_generation <> 1
       OR target_contract <> expected_contract
       OR target_dimensions <> 3072
       OR target_strategy <> 'halfvec_hnsw'
       OR target_operator_class <> 'halfvec_cosine_ops'
       OR target_expression <> 'embedding::halfvec(3072)'
       OR target_state <> 'deprecated'
       OR target_activated_at IS NULL
    THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: deprecated generation metadata does not match the verified target',
            target_name;
    END IF;

    SELECT count(*)
      INTO active_generation_count
      FROM public.search_index_generations
     WHERE embedding_contract_id = expected_contract
       AND activation_state = 'active';
    IF active_generation_count <> 1 THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: expected one active generation for the verified contract, found %',
            target_name, active_generation_count;
    END IF;

    SELECT count(*)
      INTO active_generation_count
      FROM public.search_index_generations
     WHERE physical_index_name = active_name;
    IF active_generation_count <> 1 THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: expected one matching active generation, found %',
            target_name, active_generation_count;
    END IF;

    SELECT generation, embedding_contract_id, embedding_dimensions,
           ann_strategy, operator_class, indexed_expression, activation_state,
           activated_at
      INTO active_generation, active_contract, active_dimensions,
           active_strategy, active_operator_class, active_expression,
           active_state, active_activated_at
      FROM public.search_index_generations
     WHERE physical_index_name = active_name;

    IF active_generation <> 2
       OR active_contract <> expected_contract
       OR active_dimensions <> 3072
       OR active_strategy <> 'halfvec_hnsw'
       OR active_operator_class <> 'halfvec_cosine_ops'
       OR active_expression <> 'embedding::halfvec(3072)'
       OR active_state <> 'active'
       OR active_activated_at IS NULL
    THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: active replacement metadata does not match the verified target',
            target_name;
    END IF;

    SELECT lifecycle_state
      INTO active_contract_state
      FROM public.embedding_contracts
     WHERE embedding_contract_id = expected_contract
       AND dimensions = 3072;
    IF active_contract_state IS DISTINCT FROM 'active' THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: the verified embedding contract is not active',
            target_name;
    END IF;

    expected_definition := format(
        'create index __dense_mem_index__ on public.search_documents using hnsw (((embedding)::halfvec(3072)) halfvec_cosine_ops) with (m=''16'', ef_construction=''64'') where ((embedding_contract_id = ''%s''::uuid) and (embedding_dimensions = 3072) and (search_state = ''current''::text) and (embedding is not null))',
        expected_contract
    );

    SELECT count(*)
      INTO incoming_dependency_count
      FROM pg_depend
     WHERE refclassid = 'pg_class'::regclass
       AND refobjid = target_oid;
    IF incoming_dependency_count <> 0 THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: unexpected dependent objects exist (% dependencies)',
            target_name, incoming_dependency_count;
    END IF;

    SELECT count(*)
      INTO constraint_count
      FROM pg_constraint
     WHERE conindid = target_oid;
    IF constraint_count <> 0 THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: it backs % constraints',
            target_name, constraint_count;
    END IF;

    SELECT count(*)
      INTO inheritance_count
      FROM pg_inherits
     WHERE inhrelid = target_oid
        OR inhparent = target_oid;
    IF inheritance_count <> 0 THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: it participates in index inheritance',
            target_name;
    END IF;

    FOR physical IN
        SELECT c.oid AS index_oid,
               c.relkind::text AS relkind,
               n.nspname AS schema_name,
               t.relname AS table_name,
               am.amname AS access_method,
               op.opcname AS operator_class,
               i.indisvalid,
               i.indisready,
               i.indislive,
               i.indisunique,
               i.indnatts,
               i.indnkeyatts,
               pg_get_expr(i.indexprs, i.indrelid) AS indexed_expression,
               pg_get_expr(i.indpred, i.indrelid) AS predicate,
               c.reloptions,
               pg_get_indexdef(c.oid) AS definition
          FROM pg_class AS c
          JOIN pg_namespace AS n ON n.oid = c.relnamespace
          JOIN pg_index AS i ON i.indexrelid = c.oid
          JOIN pg_class AS t ON t.oid = i.indrelid
          JOIN pg_am AS am ON am.oid = c.relam
          JOIN pg_opclass AS op ON op.oid = i.indclass[0]
         WHERE c.oid IN (target_oid, active_oid)
    LOOP
        physical_count := physical_count + 1;
        IF physical.relkind <> 'i'
           OR physical.schema_name <> 'public'
           OR physical.table_name <> 'search_documents'
           OR physical.access_method <> 'hnsw'
           OR physical.operator_class <> 'halfvec_cosine_ops'
           OR NOT physical.indisvalid
           OR NOT physical.indisready
           OR NOT physical.indislive
           OR physical.indisunique
           OR physical.indnatts <> 1
           OR physical.indnkeyatts <> 1
           OR physical.indexed_expression <> '(embedding)::halfvec(3072)'
           OR physical.predicate <> format(
               '((embedding_contract_id = ''%s''::uuid) AND (embedding_dimensions = 3072) AND (search_state = ''current''::text) AND (embedding IS NOT NULL))',
               expected_contract
           )
           OR physical.reloptions IS DISTINCT FROM ARRAY['m=16', 'ef_construction=64']::text[]
        THEN
            RAISE EXCEPTION
                'cannot retire ANN index %: physical definition is not the verified halfvec HNSW definition',
                target_name;
        END IF;

        normalized_definition := regexp_replace(
            regexp_replace(lower(physical.definition), '\s+', ' ', 'g'),
            '^create index [^ ]+ on ',
            'create index __dense_mem_index__ on '
        );
        IF normalized_definition <> expected_definition THEN
            RAISE EXCEPTION
                'cannot retire ANN index %: catalog definition mismatch',
                target_name;
        END IF;
    END LOOP;

    IF physical_count <> 2 THEN
        RAISE EXCEPTION
            'cannot retire ANN index %: expected both target and active catalog objects, found %',
            target_name, physical_count;
    END IF;

    DROP INDEX public.v2_search_1a06f88de92e_3072_halfvec_hnsw_idx RESTRICT;
END
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION
        'cannot automatically roll back 20260910020001: restore the captured catalog definition for v2_search_1a06f88de92e_3072_halfvec_hnsw_idx before retrying';
END
$$;

-- Exact restoration definition (run only after verifying the target is absent):
-- CREATE INDEX v2_search_1a06f88de92e_3072_halfvec_hnsw_idx
--     ON public.search_documents
--     USING hnsw (((embedding)::halfvec(3072)) halfvec_cosine_ops)
--     WITH (m='16', ef_construction='64')
--     WHERE ((embedding_contract_id = '1a06f88d-e92e-4f3c-8d27-0b747bd8cf17'::uuid)
--        AND (embedding_dimensions = 3072)
--        AND (search_state = 'current'::text)
--        AND (embedding IS NOT NULL));

-- +goose StatementEnd
