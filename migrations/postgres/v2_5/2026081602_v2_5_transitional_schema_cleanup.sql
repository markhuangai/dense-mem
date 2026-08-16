-- +goose Up
-- +goose StatementBegin

-- This is the irreversible v2.5 transitional-schema cleanup.
-- Lock/rewrite impact: each direct FK is added NOT VALID, validated with a table scan,
-- then replaces its mirror FK. ALTER TABLE locks are held until commit; no heap
-- rewrite occurs. The largest expected validation scan is search_documents.
-- WAL/recovery: constraint catalog changes and obsolete-table drops are small;
-- validation reads the source tables. Any failure rolls back the whole cleanup.
-- RLS impact: migration mode is explicit. Direct FKs preserve ON DELETE RESTRICT;
-- a temporary teams SELECT policy permits the preflight through FORCE RLS;
-- terminal-job deletion is granted only to system/migration transaction modes.
-- Backfill: none; preflight validation requires canonical team and owner rows to exist.
-- Backward compatibility: deploy only after runtime dependencies on the removed tables are gone.
-- Rollback: restore from the pre-migration backup; the down migration is blocked.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('lock_timeout', '30s', true);

DROP POLICY IF EXISTS embedding_jobs_system_delete ON embedding_jobs;
CREATE POLICY embedding_jobs_system_delete ON embedding_jobs
    FOR DELETE
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));

DROP POLICY IF EXISTS teams_v25_cleanup_migration_read ON teams;
CREATE POLICY teams_v25_cleanup_migration_read ON teams
    FOR SELECT
    USING (current_setting('app.tx_mode', true) = 'migration');

DO $dense_mem_v25_schema_preflight$
DECLARE
    expected_dependencies CONSTANT TEXT[] := ARRAY[
        'community_snapshot_runs.community_snapshot_runs_team_id_fkey',
        'dream_cycle_runs.dream_cycle_runs_team_id_initiated_by_profile_id_fkey',
        'embedding_jobs.embedding_jobs_team_id_owner_profile_id_fkey',
        'entity_correction_events.entity_correction_events_team_id_owner_profile_id_fkey',
        'entity_correction_plans.entity_correction_plans_team_id_owner_profile_id_fkey',
        'entity_names.entity_names_team_id_owner_profile_id_fkey',
        'entity_records.entity_records_team_id_fkey',
        'entity_resolution_events.entity_resolution_events_team_id_owner_profile_id_fkey',
        'evidence_fragments.evidence_fragments_team_id_owner_profile_id_fkey',
        'evidence_lifecycle_operations.evidence_lifecycle_operations_actor_profile_fk',
        'evidence_lifecycle_operations.evidence_lifecycle_operations_team_id_owner_profile_id_fkey',
        'evidence_quarantines.evidence_quarantines_team_id_owner_profile_id_fkey',
        'evidence_quarantines.evidence_quarantines_team_id_released_by_profile_id_fkey',
        'evidence_security_events.evidence_security_events_team_id_actor_profile_id_fkey',
        'evidence_security_events.evidence_security_events_team_id_owner_profile_id_fkey',
        'evidence_security_signals.evidence_security_signals_team_id_owner_profile_id_fkey',
        'evidence_source_revisions.evidence_source_revisions_team_id_owner_profile_id_fkey',
        'evidence_sources.evidence_sources_team_id_owner_profile_id_fkey',
        'hypotheses.hypotheses_team_id_created_by_profile_id_fkey',
        'hypothesis_feedback_events.hypothesis_feedback_events_team_id_actor_profile_id_fkey',
        'knowledge_ingests.knowledge_ingests_team_id_owner_profile_id_fkey',
        'placement_items.placement_items_team_id_owner_profile_id_fkey',
        'placement_outcomes.placement_outcomes_team_id_owner_profile_id_fkey',
        'placement_runs.placement_runs_team_id_owner_profile_id_fkey',
        'relationship_conflict_derived_evidence_tasks.relationship_conflict_derived_ev_team_id_system_profile_id_fkey',
        'relationship_conflict_events.relationship_conflict_events_team_id_actor_profile_id_fkey',
        'relationship_conflict_evidence_derivations.relationship_conflict_evidence_d_team_id_system_profile_id_fkey',
        'relationship_correction_submissions.relationship_correction_submissions_owner_fk',
        'relationship_cross_references.relationship_cross_references_team_id_author_profile_id_fkey',
        'relationship_evidence_supports.relationship_evidence_supports_team_id_owner_profile_id_fkey',
        'relationship_observations.relationship_observations_team_id_owner_profile_id_fkey',
        'relationship_records.relationship_records_team_id_owner_profile_id_fkey',
        'relationship_support_decision_events.relationship_support_decision_eve_team_id_actor_profile_id_fkey',
        'relationship_support_decision_events.relationship_support_decision_eve_team_id_owner_profile_id_fkey',
        'relationship_transition_events.relationship_transition_events_team_id_owner_profile_id_fkey',
        'review_tasks.review_tasks_team_id_owner_profile_id_fkey',
        'search_documents.search_documents_team_id_owner_profile_id_fkey',
        'search_projection_generations.search_projection_generations_team_id_fkey',
        'semantic_profile_refs.semantic_profile_refs_team_id_fkey',
        'submission_holds.submission_holds_team_id_owner_profile_id_fkey',
        'team_predicate_definitions.team_predicate_definitions_team_id_fkey',
        'value_records.value_records_team_id_fkey',
        'verification_events.verification_events_team_id_owner_profile_id_fkey'
    ];
    actual_dependencies TEXT[];
    mismatch_count BIGINT;
BEGIN
    SELECT array_agg(source.relname || '.' || constraint_state.conname
                     ORDER BY source.relname || '.' || constraint_state.conname)
    INTO actual_dependencies
    FROM pg_constraint AS constraint_state
    JOIN pg_class AS source ON source.oid = constraint_state.conrelid
    WHERE constraint_state.contype = 'f'
      AND constraint_state.confrelid IN (
          'semantic_team_refs'::regclass,
          'semantic_profile_refs'::regclass
      );

    IF actual_dependencies IS DISTINCT FROM expected_dependencies THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: semantic mirror FK catalog differs from the reviewed contract';
    END IF;

    SELECT count(*)
    INTO mismatch_count
    FROM pg_constraint AS constraint_state
    WHERE constraint_state.contype = 'f'
      AND constraint_state.confrelid IN (
          'semantic_team_refs'::regclass,
          'semantic_profile_refs'::regclass
      )
      AND (
          (
              constraint_state.conrelid = 'semantic_profile_refs'::regclass
              AND constraint_state.confdeltype <> 'c'
          )
          OR (
              constraint_state.conrelid <> 'semantic_profile_refs'::regclass
              AND constraint_state.confdeltype <> 'r'
          )
          OR constraint_state.confupdtype <> 'a'
          OR constraint_state.confmatchtype <> 's'
          OR constraint_state.condeferrable
          OR NOT constraint_state.convalidated
      );
    IF mismatch_count > 0 THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: semantic mirror FK behavior differs (% constraints)', mismatch_count;
    END IF;

    SELECT count(*)
    INTO mismatch_count
    FROM semantic_team_refs AS mirror
    LEFT JOIN teams AS team ON team.id = mirror.team_id
    WHERE team.id IS NULL;
    IF mismatch_count > 0 THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: semantic teams missing canonical teams (% rows)', mismatch_count;
    END IF;

    SELECT count(*)
    INTO mismatch_count
    FROM semantic_profile_refs AS mirror
    LEFT JOIN ownership_aliases AS alias
      ON alias.team_id = mirror.team_id
     AND alias.legacy_owner_id = mirror.profile_id
    WHERE alias.legacy_owner_id IS NULL;
    IF mismatch_count > 0 THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: semantic owners missing permanent aliases (% rows)', mismatch_count;
    END IF;
END;
$dense_mem_v25_schema_preflight$;

DO $dense_mem_v25_embedding_preflight$
DECLARE
    legacy_count BIGINT;
    legacy_model TEXT;
    legacy_dimensions INTEGER;
    active_count BIGINT;
    active_model TEXT;
    active_dimensions INTEGER;
    search_row_count BIGINT;
BEGIN
    SELECT count(*), max(model), max(dimensions)
    INTO legacy_count, legacy_model, legacy_dimensions
    FROM embedding_config;
    IF legacy_count > 1
       OR (legacy_count = 1 AND (btrim(legacy_model) = '' OR legacy_dimensions < 1)) THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: legacy embedding configuration is invalid';
    END IF;

    SELECT count(*), max(model), max(dimensions)
    INTO active_count, active_model, active_dimensions
    FROM embedding_contracts
    WHERE lifecycle_state = 'active';
    IF active_count > 1 THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: expected at most one active embedding contract, found %', active_count;
    END IF;

    SELECT
        (SELECT count(*) FROM search_documents)
      + (SELECT count(*) FROM embedding_jobs)
      + (SELECT count(*) FROM search_index_generations)
    INTO search_row_count;
    IF active_count = 0 AND search_row_count > 0 THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: search data exists without an active embedding contract';
    END IF;

    IF legacy_count = 1
       AND active_count = 1
       AND (legacy_model <> active_model OR legacy_dimensions <> active_dimensions) THEN
        RAISE EXCEPTION 'v2.5 schema cleanup blocked: embedding_config does not match the active embedding contract';
    END IF;
END;
$dense_mem_v25_embedding_preflight$;

DO $dense_mem_v25_replace_semantic_fks$
DECLARE
    fk RECORD;
    source_columns TEXT;
    temporary_name TEXT;
    direct_target TEXT;
BEGIN
    FOR fk IN
        SELECT constraint_state.oid,
               source_namespace.nspname AS source_schema,
               source.relname AS source_table,
               target.relname AS target_table,
               constraint_state.conname
        FROM pg_constraint AS constraint_state
        JOIN pg_class AS source ON source.oid = constraint_state.conrelid
        JOIN pg_namespace AS source_namespace ON source_namespace.oid = source.relnamespace
        JOIN pg_class AS target ON target.oid = constraint_state.confrelid
        WHERE constraint_state.contype = 'f'
          AND constraint_state.confrelid IN (
              'semantic_team_refs'::regclass,
              'semantic_profile_refs'::regclass
          )
          AND constraint_state.conrelid <> 'semantic_profile_refs'::regclass
        ORDER BY target.relname, source.relname, constraint_state.conname
    LOOP
        SELECT string_agg(quote_ident(attribute.attname), ', ' ORDER BY key_column.ordinality)
        INTO source_columns
        FROM pg_constraint AS constraint_state
        CROSS JOIN LATERAL unnest(constraint_state.conkey)
            WITH ORDINALITY AS key_column(attnum, ordinality)
        JOIN pg_attribute AS attribute
          ON attribute.attrelid = constraint_state.conrelid
         AND attribute.attnum = key_column.attnum
        WHERE constraint_state.oid = fk.oid;

        temporary_name := 'dense_mem_v25_' || substr(md5(fk.source_table || '.' || fk.conname), 1, 20);
        IF fk.target_table = 'semantic_team_refs' THEN
            direct_target := 'teams(id)';
        ELSE
            direct_target := 'ownership_aliases(team_id, legacy_owner_id)';
        END IF;

        EXECUTE format(
            'ALTER TABLE %I.%I ADD CONSTRAINT %I FOREIGN KEY (%s) REFERENCES %s ON DELETE RESTRICT NOT VALID',
            fk.source_schema, fk.source_table, temporary_name, source_columns, direct_target
        );
        EXECUTE format(
            'ALTER TABLE %I.%I VALIDATE CONSTRAINT %I',
            fk.source_schema, fk.source_table, temporary_name
        );
        EXECUTE format(
            'ALTER TABLE %I.%I DROP CONSTRAINT %I',
            fk.source_schema, fk.source_table, fk.conname
        );
        EXECUTE format(
            'ALTER TABLE %I.%I RENAME CONSTRAINT %I TO %I',
            fk.source_schema, fk.source_table, temporary_name, fk.conname
        );
    END LOOP;
END;
$dense_mem_v25_replace_semantic_fks$;

DROP TABLE semantic_profile_refs;
DROP TABLE semantic_team_refs;
DROP TABLE embedding_config;
DROP POLICY teams_v25_cleanup_migration_read ON teams;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $dense_mem_irreversible_v25_schema_cleanup$
BEGIN
    RAISE EXCEPTION 'irreversible migration: v2.5 schema cleanup removed semantic mirror references and embedding_config';
END;
$dense_mem_irreversible_v25_schema_cleanup$;

-- +goose StatementEnd
