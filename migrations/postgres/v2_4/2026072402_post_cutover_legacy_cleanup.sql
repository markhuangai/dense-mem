-- +goose Up
-- +goose StatementBegin

-- This cleanup follows the already-released 2026072401 migration.
SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Lock/rewrite: dropped legacy tables take ACCESS EXCLUSIVE locks on those
-- tables only. The fresh-install guard takes SHARE locks while it proves that
-- application-owned tables are empty.
-- WAL/disk: table drops remove legacy heap/index files; no retained table is
-- rewritten except the metadata-only placement_runs column drop.
-- RLS/roles: retained migration audit tables become system-readable and
-- immutable through runtime RLS. Fresh authority bootstrap may insert the
-- compatible marker after migrations complete.
-- Rollback: irreversible by design because the release removes legacy runtime
-- paths and legacy table definitions are no longer authoritative.
DO $$
DECLARE
    app_tables TEXT[] := ARRAY[
        'profiles',
        'api_keys',
        'teams',
        'team_profiles',
        'audit_log',
        'usage_metric_buckets',
        'operation_logs',
        'recall_feedback_events',
        'security_ip_failures',
        'security_ip_bans',
        'memory_placement_runs',
        'memory_placement_items',
        'memory_dispute_sessions',
        'skill_pack_imports',
        'skill_pack_import_changes',
        'sso_providers',
        'sso_identities',
        'sso_group_mappings',
        'sso_entitlement_cache',
        'sso_oauth_states',
        'sso_sessions',
        'community_detection_runs',
        'semantic_team_refs',
        'team_predicate_definitions',
        'semantic_profile_refs',
        'knowledge_ingests',
        'evidence_sources',
        'evidence_source_revisions',
        'evidence_fragments',
        'evidence_security_events',
        'evidence_security_signals',
        'evidence_quarantines',
        'placement_runs',
        'placement_items',
        'placement_outcomes',
        'entity_records',
        'entity_names',
        'value_records',
        'relationship_records',
        'relationship_observations',
        'verification_events',
        'relationship_evidence_supports',
        'relationship_support_decision_events',
        'relationship_transition_events',
        'entity_resolution_events',
        'entity_correction_events',
        'relationship_cross_references',
        'entity_correction_plans',
        'review_tasks',
        'hypotheses',
        'embedding_contracts',
        'search_index_generations',
        'search_documents',
        'embedding_jobs',
        'community_snapshot_runs',
        'community_records',
        'community_memberships',
        'community_sources',
        'dream_cycle_runs',
        'v2_migration_runs',
        'v2_migration_corpus_items',
        'v2_migration_source_maps',
        'v2_migration_checkpoints',
        'v2_migration_errors',
        'v2_migration_exclusions',
        'v2_migration_gate_results',
        'v2_migration_operator_actions',
        'v2_compatibility_markers'
    ];
    tbl_name TEXT;
    has_rows BOOLEAN;
    has_compatible_marker BOOLEAN := false;
    nonempty_tables TEXT[] := ARRAY[]::TEXT[];
    missing_profiles INTEGER := 0;
    missing_api_keys INTEGER := 0;
    api_key_has_id BOOLEAN := false;
    api_key_has_team_id BOOLEAN := false;
    api_key_has_profile_id BOOLEAN := false;
    api_key_owner_column TEXT := '';
BEGIN
    IF to_regclass('public.v2_compatibility_markers') IS NOT NULL THEN
        SELECT EXISTS (
            SELECT 1
            FROM v2_compatibility_markers
            WHERE marker_kind = 'v2_cutover'
              AND status = 'compatible'
        ) INTO has_compatible_marker;
    END IF;

    IF has_compatible_marker THEN
        IF to_regclass('public.profiles') IS NOT NULL THEN
            LOCK TABLE profiles IN SHARE MODE;
            LOCK TABLE teams IN SHARE MODE;

            SELECT count(*)::int
            INTO missing_profiles
            FROM profiles AS legacy
            LEFT JOIN teams AS canonical
              ON canonical.id = legacy.id
            WHERE canonical.id IS NULL;

            IF missing_profiles > 0 THEN
                RAISE EXCEPTION 'post-cutover cleanup blocked: legacy profiles missing canonical teams rows (% rows)', missing_profiles;
            END IF;
        END IF;

        IF to_regclass('public.api_keys') IS NOT NULL THEN
            LOCK TABLE api_keys IN SHARE MODE;
            LOCK TABLE team_profiles IN SHARE MODE;

            SELECT EXISTS (
                SELECT 1
                FROM information_schema.columns
                WHERE table_schema = 'public'
                  AND table_name = 'api_keys'
                  AND column_name = 'id'
            ) INTO api_key_has_id;

            SELECT EXISTS (
                SELECT 1
                FROM information_schema.columns
                WHERE table_schema = 'public'
                  AND table_name = 'api_keys'
                  AND column_name = 'team_id'
            ) INTO api_key_has_team_id;

            SELECT EXISTS (
                SELECT 1
                FROM information_schema.columns
                WHERE table_schema = 'public'
                  AND table_name = 'api_keys'
                  AND column_name = 'profile_id'
            ) INTO api_key_has_profile_id;

            IF NOT api_key_has_id OR (NOT api_key_has_team_id AND NOT api_key_has_profile_id) THEN
                RAISE EXCEPTION 'post-cutover cleanup blocked: legacy api_keys table has unsupported shape';
            END IF;

            api_key_owner_column := CASE WHEN api_key_has_team_id THEN 'team_id' ELSE 'profile_id' END;
            EXECUTE format($sql$
                SELECT count(*)::int
                FROM api_keys AS legacy
                LEFT JOIN team_profiles AS canonical
                  ON canonical.id = legacy.id
                 AND canonical.team_id = legacy.%I
                WHERE canonical.id IS NULL
            $sql$, api_key_owner_column)
            INTO missing_api_keys;

            IF missing_api_keys > 0 THEN
                RAISE EXCEPTION 'post-cutover cleanup blocked: legacy api_keys missing canonical team_profiles rows (% rows)', missing_api_keys;
            END IF;
        END IF;
    ELSE
        FOREACH tbl_name IN ARRAY app_tables LOOP
            IF to_regclass(format('public.%I', tbl_name)) IS NULL THEN
                CONTINUE;
            END IF;

            EXECUTE format('LOCK TABLE public.%I IN SHARE MODE', tbl_name);
            EXECUTE format('SELECT EXISTS (SELECT 1 FROM public.%I LIMIT 1)', tbl_name)
            INTO has_rows;

            IF has_rows THEN
                nonempty_tables := array_append(nonempty_tables, tbl_name);
            END IF;
        END LOOP;

        IF array_length(nonempty_tables, 1) IS NOT NULL THEN
            RAISE EXCEPTION 'post-cutover cleanup blocked: compatible cutover marker missing and application tables are not empty: %', array_to_string(nonempty_tables, ', ');
        END IF;
    END IF;
END $$;

DROP TABLE IF EXISTS memory_dispute_sessions;
DROP TABLE IF EXISTS memory_placement_items;
DROP TABLE IF EXISTS memory_placement_runs;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS profiles;
DROP TABLE IF EXISTS community_detection_runs;

ALTER TABLE placement_runs
    DROP COLUMN IF EXISTS migration_claim_epoch;

DO $$
DECLARE
    audit_tables TEXT[] := ARRAY[
        'v2_migration_runs',
        'v2_migration_corpus_items',
        'v2_migration_source_maps',
        'v2_migration_checkpoints',
        'v2_migration_errors',
        'v2_migration_exclusions',
        'v2_migration_gate_results',
        'v2_migration_operator_actions'
    ];
    tbl_name TEXT;
    policy_name TEXT;
BEGIN
    FOREACH tbl_name IN ARRAY audit_tables LOOP
        IF to_regclass(format('public.%I', tbl_name)) IS NULL THEN
            CONTINUE;
        END IF;

        FOR policy_name IN
            SELECT policyname
            FROM pg_policies
            WHERE schemaname = 'public'
              AND tablename = tbl_name
        LOOP
            EXECUTE format('DROP POLICY IF EXISTS %I ON public.%I', policy_name, tbl_name);
        END LOOP;

        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tbl_name);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tbl_name);
        EXECUTE format(
            'CREATE POLICY %I ON public.%I FOR SELECT TO PUBLIC USING (current_setting(''app.tx_mode'', true) = ''system'')',
            tbl_name || '_system_select',
            tbl_name
        );
    END LOOP;

    IF to_regclass('public.v2_compatibility_markers') IS NOT NULL THEN
        FOR policy_name IN
            SELECT policyname
            FROM pg_policies
            WHERE schemaname = 'public'
              AND tablename = 'v2_compatibility_markers'
        LOOP
            EXECUTE format('DROP POLICY IF EXISTS %I ON public.v2_compatibility_markers', policy_name);
        END LOOP;

        ALTER TABLE v2_compatibility_markers ENABLE ROW LEVEL SECURITY;
        ALTER TABLE v2_compatibility_markers FORCE ROW LEVEL SECURITY;

        CREATE POLICY v2_compatibility_markers_system_select ON v2_compatibility_markers
            FOR SELECT
            TO PUBLIC
            USING (current_setting('app.tx_mode', true) = 'system');

        CREATE POLICY v2_compatibility_markers_system_insert ON v2_compatibility_markers
            FOR INSERT
            TO PUBLIC
            WITH CHECK (
                current_setting('app.tx_mode', true) = 'system'
                AND marker_kind = 'v2_cutover'
                AND status = 'compatible'
            );
    END IF;
END $$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION 'irreversible migration: post-cutover legacy cleanup drops retired v1 tables and runtime support';
END $$;

-- +goose StatementEnd
