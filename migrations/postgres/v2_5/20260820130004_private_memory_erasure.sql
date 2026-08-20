-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: memory_spaces and every cataloged space-owned table
-- receive additive metadata columns without rewriting historical rows. New
-- constraints are installed NOT VALID, and DDL locks are held only while each
-- column, constraint, policy, and trigger is installed.
-- RLS impact: erasure control tables are FORCE RLS and system-only. Canonical
-- and derived rows keep their existing policies; deletion runs as system mode
-- with an exact transaction-local private-space fence.
-- Backfill: existing rows with a space_id receive the catalog generation in
-- bounded, resumable batches; rows without a space remain unscoped. Audit
-- rows are associated only when a surviving credential and team prove the
-- private space; retention derives legacy private-content age later in
-- bounded, resumable batches.
-- Backward compatibility: old writers omit space_generation; the database
-- trigger supplies the current active generation and rejects sealed spaces.
-- Rollback: physical erasure is irreversible. The down migration is blocked;
-- disabling retention stops new automatic operations but cannot restore data.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);

ALTER TABLE memory_spaces
    ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS lifecycle_state TEXT NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS private_content_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS sealed_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS retired_at TIMESTAMPTZ NULL;

ALTER TABLE memory_spaces DROP CONSTRAINT IF EXISTS memory_spaces_generation_positive;
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_generation_positive CHECK (generation > 0) NOT VALID;
ALTER TABLE memory_spaces DROP CONSTRAINT IF EXISTS memory_spaces_lifecycle_state_check;
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_lifecycle_state_check
    CHECK (lifecycle_state IN ('active', 'sealed', 'retired')) NOT VALID;
ALTER TABLE memory_spaces DROP CONSTRAINT IF EXISTS memory_spaces_team_shared_active;
ALTER TABLE memory_spaces ADD CONSTRAINT memory_spaces_team_shared_active
    CHECK (kind <> 'team_shared' OR (lifecycle_state = 'active' AND sealed_at IS NULL AND retired_at IS NULL)) NOT VALID;

CREATE TABLE IF NOT EXISTS private_memory_legal_holds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL,
    space_id UUID NOT NULL,
    reason_code TEXT NOT NULL CHECK (reason_code ~ '^[a-z0-9][a-z0-9_.-]{0,63}$'),
    actor_class TEXT NOT NULL CHECK (actor_class IN ('control')),
    placed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ NULL,
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX IF NOT EXISTS private_memory_legal_holds_active_unique
    ON private_memory_legal_holds(space_id) WHERE released_at IS NULL;
CREATE INDEX IF NOT EXISTS private_memory_legal_holds_team_space_idx
    ON private_memory_legal_holds(team_id, space_id, placed_at DESC);

CREATE TABLE IF NOT EXISTS private_memory_erasure_operations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    space_id UUID NULL,
    space_kind TEXT NULL CHECK (space_kind IN ('profile_private', 'credential_private')),
    target_credential_id UUID NULL,
    action TEXT NOT NULL CHECK (action IN ('erase_profile_private', 'erase_credential_private', 'retire_credential', 'retention_purge')),
    actor_class TEXT NOT NULL CHECK (actor_class IN ('owner_sso', 'owner_credential', 'control', 'retention')),
    reason_code TEXT NOT NULL CHECK (reason_code ~ '^[a-z0-9][a-z0-9_.-]{0,63}$'),
    target_generation BIGINT NULL CHECK (target_generation > 0),
    retire_space BOOLEAN NOT NULL DEFAULT false,
    idempotency_scope_hash TEXT NOT NULL CHECK (length(idempotency_scope_hash) = 64),
    request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'processing', 'completed', 'failed')),
    manifest_position INTEGER NOT NULL DEFAULT 0 CHECK (manifest_position >= 0),
    deleted_counts JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(deleted_counts) = 'object'),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    fence BIGINT NOT NULL DEFAULT 0 CHECK (fence >= 0),
    worker_id TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ NULL,
    next_attempt_at TIMESTAMPTZ NULL,
    last_error_code TEXT NOT NULL DEFAULT '',
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT private_memory_erasure_target_shape CHECK (
        (space_id IS NOT NULL AND space_kind IS NOT NULL AND target_generation IS NOT NULL)
        OR (space_id IS NULL AND space_kind IS NULL AND target_generation IS NULL AND action = 'retire_credential')
    ),
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX IF NOT EXISTS private_memory_erasure_idempotency_unique
    ON private_memory_erasure_operations(idempotency_scope_hash);
CREATE INDEX IF NOT EXISTS private_memory_erasure_claim_idx
    ON private_memory_erasure_operations(status, next_attempt_at, lease_until, requested_at, id)
    WHERE status IN ('queued', 'processing');
CREATE UNIQUE INDEX IF NOT EXISTS private_memory_erasure_space_active_unique
    ON private_memory_erasure_operations(space_id)
    WHERE space_id IS NOT NULL AND status IN ('queued', 'processing');

CREATE TABLE IF NOT EXISTS private_memory_retention_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_class TEXT NOT NULL CHECK (actor_class IN ('control', 'retention')),
    idempotency_scope_hash TEXT NOT NULL CHECK (length(idempotency_scope_hash) = 64),
    request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
    cutoff TIMESTAMPTZ NOT NULL,
    retention_days INTEGER NOT NULL CHECK (retention_days > 0),
    queued_count INTEGER NOT NULL DEFAULT 0 CHECK (queued_count >= 0),
    status TEXT NOT NULL CHECK (status IN ('completed')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS private_memory_retention_runs_idempotency_unique
    ON private_memory_retention_runs(idempotency_scope_hash);
CREATE INDEX IF NOT EXISTS private_memory_retention_runs_started_idx
    ON private_memory_retention_runs(started_at DESC, id DESC);

DO $dense_mem_private_tables_rls$
DECLARE
    target_table TEXT;
BEGIN
    FOREACH target_table IN ARRAY ARRAY[
        'private_memory_legal_holds',
        'private_memory_erasure_operations',
        'private_memory_retention_runs'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', target_table);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', target_table);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', target_table || '_system_access', target_table);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR ALL USING (current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')) WITH CHECK (current_setting(''app.tx_mode'', true) IN (''system'', ''migration''))',
            target_table || '_system_access',
            target_table
        );
    END LOOP;
END;
$dense_mem_private_tables_rls$;

ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS memory_space_id UUID NULL;
ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_memory_space_id_fkey;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_memory_space_id_fkey
    FOREIGN KEY (memory_space_id) REFERENCES memory_spaces(id) ON DELETE RESTRICT NOT VALID;
DROP POLICY IF EXISTS audit_log_private_erasure_update ON audit_log;
CREATE POLICY audit_log_private_erasure_update ON audit_log
    FOR UPDATE
    USING (
        current_setting('app.tx_mode', true) = 'system'
        AND memory_space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'system'
        AND memory_space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
    );

CREATE OR REPLACE FUNCTION prevent_audit_log_mutation()
RETURNS TRIGGER AS $$
BEGIN
	IF TG_OP = 'UPDATE'
	   AND current_setting('app.tx_mode', true) = 'migration'
	   AND OLD.memory_space_id IS NULL
	   AND NEW.memory_space_id IS NOT NULL
	   AND (to_jsonb(NEW) - ARRAY['memory_space_id'])
	       = (to_jsonb(OLD) - ARRAY['memory_space_id']) THEN
		RETURN NEW;
	END IF;
    IF TG_OP = 'UPDATE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = OLD.memory_space_id
       AND NEW.before_payload IS NULL
       AND NEW.after_payload IS NULL
       AND NEW.metadata = '{"private_content_erased": true}'::jsonb
       AND (to_jsonb(NEW) - ARRAY['before_payload', 'after_payload', 'metadata', 'updated_at'])
           = (to_jsonb(OLD) - ARRAY['before_payload', 'after_payload', 'metadata', 'updated_at']) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION prevent_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND (
           NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               = NULLIF(to_jsonb(OLD)->>'space_id', '')::uuid
           OR (
               TG_TABLE_NAME = 'relationship_cross_references'
               AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = (
                   SELECT target.space_id
                   FROM relationship_records AS target
                   WHERE target.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND target.relationship_id = NULLIF(to_jsonb(OLD)->>'target_relationship_id', '')::uuid
               )
           )
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION dense_mem_memory_space_defaults()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    current_generation BIGINT;
    current_state TEXT;
    current_space_team UUID;
    previous_mode TEXT;
    previous_team TEXT;
BEGIN
    IF NEW.space_id IS NULL AND NEW.team_id IS NOT NULL THEN
        NEW.space_id := dense_mem_team_shared_space(NEW.team_id);
    END IF;
    IF NEW.space_id IS NULL THEN
        NEW.space_generation := NULL;
        RETURN NEW;
    END IF;
    previous_mode := current_setting('app.tx_mode', true);
    previous_team := current_setting('app.current_team_id', true);
    IF previous_mode IN ('team', 'profile')
       AND (NEW.team_id IS NULL OR NULLIF(previous_team, '')::uuid IS DISTINCT FROM NEW.team_id) THEN
        RAISE EXCEPTION 'memory space write is outside the authenticated team';
    ELSIF previous_mode NOT IN ('team', 'profile', 'system', 'migration') THEN
        RAISE EXCEPTION 'memory space write requires an authenticated transaction';
    END IF;
    PERFORM set_config('app.tx_mode', 'system', true);
    SELECT generation, lifecycle_state, team_id
    INTO current_generation, current_state, current_space_team
    FROM memory_spaces
    WHERE id = NEW.space_id
    FOR KEY SHARE;
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    IF current_generation IS NULL OR current_space_team IS DISTINCT FROM NEW.team_id THEN
        RAISE EXCEPTION 'memory space does not belong to row team';
    END IF;
    IF current_state <> 'active' THEN
        RAISE EXCEPTION 'memory space is not writable';
    END IF;
    IF NEW.space_generation IS NULL OR NEW.space_generation = 0 THEN
        NEW.space_generation := current_generation;
    ELSIF NEW.space_generation IS DISTINCT FROM current_generation THEN
        RAISE EXCEPTION 'memory space generation is stale';
    END IF;
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RAISE;
END;
$$;

CREATE OR REPLACE FUNCTION dense_mem_team_shared_generation(p_team_id UUID)
RETURNS BIGINT
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
    SELECT generation
    FROM memory_spaces
    WHERE id = dense_mem_team_shared_space(p_team_id)
$$;

DO $dense_mem_space_generation$
DECLARE
    target_table TEXT;
    delete_policy_name TEXT;
    generation_select_policy CONSTANT TEXT := 'dense_mem_20260820130004_generation_select';
    generation_update_policy CONSTANT TEXT := 'dense_mem_20260820130004_generation_update';
    target_tables CONSTANT TEXT[] := ARRAY[
        'knowledge_ingests', 'evidence_sources', 'evidence_source_revisions',
        'evidence_fragments', 'evidence_security_events', 'evidence_security_signals',
        'evidence_quarantines', 'evidence_lifecycle_operations', 'evidence_lifecycle_events',
        'placement_runs', 'placement_items', 'placement_outcomes', 'placement_assessments', 'predicate_registration_events',
        'entity_records', 'entity_names', 'entity_resolution_events',
        'entity_correction_plans', 'entity_correction_events', 'value_records',
        'relationship_records', 'relationship_observations', 'relationship_evidence_supports',
        'relationship_support_decision_events', 'relationship_transition_events',
        'relationship_cross_references', 'relationship_correction_submissions',
        'relationship_correction_events', 'verification_events', 'review_tasks',
        'hypotheses', 'hypothesis_derivation_sources', 'hypothesis_feedback_events',
        'submission_holds', 'submission_quarantine_payloads', 'submission_quarantine_tombstones', 'memory_placement_runs',
        'memory_placement_items', 'memory_dispute_sessions', 'relationship_conflict_cases',
        'relationship_conflict_positions', 'relationship_conflict_position_members',
        'relationship_conflict_events', 'relationship_conflict_review_runs',
        'relationship_conflict_derived_evidence_tasks', 'relationship_conflict_evidence_derivations',
        'relationship_conflict_resolution_plans', 'relationship_conflict_ai_assessment_attempts',
        'relationship_conflict_ai_assessment_events', 'search_documents', 'embedding_jobs',
        'community_snapshot_runs', 'community_records', 'community_memberships',
        'community_sources', 'community_summary_attempts', 'dream_cycle_runs',
        'dream_path_evaluations', 'recall_feedback_events'
    ];
BEGIN
    FOREACH target_table IN ARRAY target_tables LOOP
        IF to_regclass(format('public.%I', target_table)) IS NULL OR NOT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND table_name = target_table AND column_name = 'space_id'
        ) THEN
            CONTINUE;
        END IF;
        EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS space_generation BIGINT NULL', target_table);
        EXECUTE format('ALTER TABLE %I DROP CONSTRAINT IF EXISTS %I', target_table, target_table || '_space_gen_ck');
        EXECUTE format(
            'ALTER TABLE %I ADD CONSTRAINT %I CHECK ((space_id IS NULL AND space_generation IS NULL) OR (space_id IS NOT NULL AND (space_generation IS NULL OR space_generation > 0))) NOT VALID',
            target_table,
            target_table || '_space_gen_ck'
        );
        EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', target_table || '_memory_space_defaults', target_table);
        EXECUTE format(
            'CREATE TRIGGER %I BEFORE INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_memory_space_defaults()',
            target_table || '_memory_space_defaults',
            target_table
        );
        delete_policy_name := left(target_table, 42) || '_pe_' || left(md5(target_table), 8);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', delete_policy_name, target_table);
        IF target_table = 'relationship_cross_references' THEN
            EXECUTE format(
                'CREATE POLICY %I ON %I FOR DELETE USING (
                    current_setting(''app.tx_mode'', true) = ''system''
                    AND (
                        %I.space_id = NULLIF(current_setting(''app.private_erasure_space_id'', true), '''')::uuid
                        OR EXISTS (
                            SELECT 1
                            FROM relationship_records AS target_relationship
                            WHERE target_relationship.team_id = %I.team_id
                              AND target_relationship.relationship_id = %I.target_relationship_id
                              AND target_relationship.space_id = NULLIF(current_setting(''app.private_erasure_space_id'', true), '''')::uuid
                        )
                    )
                )',
                delete_policy_name,
                target_table,
                target_table,
                target_table,
                target_table
            );
        ELSE
            EXECUTE format(
                'CREATE POLICY %I ON %I FOR DELETE USING (current_setting(''app.tx_mode'', true) = ''system'' AND space_id = NULLIF(current_setting(''app.private_erasure_space_id'', true), '''')::uuid)',
                delete_policy_name,
                target_table
            );
        END IF;
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', generation_select_policy, target_table);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', generation_update_policy, target_table);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (current_setting(''app.tx_mode'', true) = ''migration'')',
            generation_select_policy,
            target_table
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR UPDATE USING (current_setting(''app.tx_mode'', true) = ''migration'') WITH CHECK (current_setting(''app.tx_mode'', true) = ''migration'')',
            generation_update_policy,
            target_table
        );
    END LOOP;
END;
$dense_mem_space_generation$;

-- Existing space-scoped rows need a generation before readers begin fencing
-- by generation. Each batch runs in its own transaction so locks and temporary
-- trigger changes are bounded and a failed batch rolls back both.
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_space_generation_20260820130004()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    target_table TEXT;
    append_only_trigger RECORD;
    append_only_trigger_name TEXT;
    disabled_append_only_triggers TEXT[];
    always_append_only_triggers TEXT[];
    updated_rows INTEGER;
    target_tables CONSTANT TEXT[] := ARRAY[
        'knowledge_ingests', 'evidence_sources', 'evidence_source_revisions',
        'evidence_fragments', 'evidence_security_events', 'evidence_security_signals',
        'evidence_quarantines', 'evidence_lifecycle_operations', 'evidence_lifecycle_events',
        'placement_runs', 'placement_items', 'placement_outcomes', 'placement_assessments', 'predicate_registration_events',
        'entity_records', 'entity_names', 'entity_resolution_events',
        'entity_correction_plans', 'entity_correction_events', 'value_records',
        'relationship_records', 'relationship_observations', 'relationship_evidence_supports',
        'relationship_support_decision_events', 'relationship_transition_events',
        'relationship_cross_references', 'relationship_correction_submissions',
        'relationship_correction_events', 'verification_events', 'review_tasks',
        'hypotheses', 'hypothesis_derivation_sources', 'hypothesis_feedback_events',
        'submission_holds', 'submission_quarantine_payloads', 'submission_quarantine_tombstones', 'memory_placement_runs',
        'memory_placement_items', 'memory_dispute_sessions', 'relationship_conflict_cases',
        'relationship_conflict_positions', 'relationship_conflict_position_members',
        'relationship_conflict_events', 'relationship_conflict_review_runs',
        'relationship_conflict_derived_evidence_tasks', 'relationship_conflict_evidence_derivations',
        'relationship_conflict_resolution_plans', 'relationship_conflict_ai_assessment_attempts',
        'relationship_conflict_ai_assessment_events', 'search_documents', 'embedding_jobs',
        'community_snapshot_runs', 'community_records', 'community_memberships',
        'community_sources', 'community_summary_attempts', 'dream_cycle_runs',
        'dream_path_evaluations', 'recall_feedback_events'
    ];
BEGIN
    FOREACH target_table IN ARRAY target_tables LOOP
        IF to_regclass(format('public.%I', target_table)) IS NULL OR NOT EXISTS (
            SELECT 1
            FROM information_schema.columns
            WHERE table_schema = 'public'
              AND table_name = target_table
              AND column_name IN ('space_id', 'space_generation')
            GROUP BY table_name
            HAVING count(*) = 2
        ) THEN
            CONTINUE;
        END IF;

        LOOP
            PERFORM set_config('app.tx_mode', 'migration', true);
            PERFORM set_config('app.current_team_id', '', true);
            PERFORM set_config('app.current_profile_id', '', true);
            PERFORM set_config('app.allowed_space_ids', '', true);
            PERFORM set_config('app.private_erasure_space_id', '', true);

            disabled_append_only_triggers := ARRAY[]::TEXT[];
            always_append_only_triggers := ARRAY[]::TEXT[];
            FOR append_only_trigger IN
                SELECT trigger_row.tgname, trigger_row.tgenabled
                FROM pg_trigger AS trigger_row
                JOIN pg_proc AS function_row ON function_row.oid = trigger_row.tgfoid
                WHERE trigger_row.tgrelid = to_regclass(format('public.%I', target_table))
                  AND NOT trigger_row.tgisinternal
                  AND function_row.proname IN ('prevent_append_only_mutation', 'prevent_v2_append_only_mutation')
            LOOP
                IF append_only_trigger.tgenabled = 'A' THEN
                    always_append_only_triggers := array_append(always_append_only_triggers, append_only_trigger.tgname);
                    EXECUTE format('ALTER TABLE %I DISABLE TRIGGER %I', target_table, append_only_trigger.tgname);
                ELSIF append_only_trigger.tgenabled = 'O' THEN
                    disabled_append_only_triggers := array_append(disabled_append_only_triggers, append_only_trigger.tgname);
                    EXECUTE format('ALTER TABLE %I DISABLE TRIGGER %I', target_table, append_only_trigger.tgname);
                END IF;
            END LOOP;

            EXECUTE format($sql$
                WITH batch AS MATERIALIZED (
                    SELECT target_row.ctid, target_row.space_id
                    FROM %I AS target_row
                    JOIN memory_spaces AS space ON space.id = target_row.space_id
                    WHERE target_row.space_id IS NOT NULL
                      AND target_row.space_generation IS NULL
                    ORDER BY target_row.ctid
                    LIMIT 500
                    FOR UPDATE OF target_row SKIP LOCKED
                )
                UPDATE %I AS target_row
                SET space_generation = space.generation
                FROM batch
                JOIN memory_spaces AS space ON space.id = batch.space_id
                WHERE target_row.ctid = batch.ctid
                  AND target_row.space_generation IS NULL
            $sql$, target_table, target_table);
            GET DIAGNOSTICS updated_rows = ROW_COUNT;

            FOREACH append_only_trigger_name IN ARRAY disabled_append_only_triggers LOOP
                EXECUTE format('ALTER TABLE %I ENABLE TRIGGER %I', target_table, append_only_trigger_name);
            END LOOP;
            FOREACH append_only_trigger_name IN ARRAY always_append_only_triggers LOOP
                EXECUTE format('ALTER TABLE %I ENABLE ALWAYS TRIGGER %I', target_table, append_only_trigger_name);
            END LOOP;

            COMMIT;
            EXIT WHEN updated_rows = 0;
        END LOOP;
    END LOOP;
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_space_generation_20260820130004();
DROP PROCEDURE IF EXISTS dense_mem_backfill_space_generation_20260820130004();

-- +goose StatementBegin
DO $dense_mem_space_generation_cleanup$
DECLARE
    target_table TEXT;
    target_tables CONSTANT TEXT[] := ARRAY[
        'knowledge_ingests', 'evidence_sources', 'evidence_source_revisions',
        'evidence_fragments', 'evidence_security_events', 'evidence_security_signals',
        'evidence_quarantines', 'evidence_lifecycle_operations', 'evidence_lifecycle_events',
        'placement_runs', 'placement_items', 'placement_outcomes', 'placement_assessments', 'predicate_registration_events',
        'entity_records', 'entity_names', 'entity_resolution_events',
        'entity_correction_plans', 'entity_correction_events', 'value_records',
        'relationship_records', 'relationship_observations', 'relationship_evidence_supports',
        'relationship_support_decision_events', 'relationship_transition_events',
        'relationship_cross_references', 'relationship_correction_submissions',
        'relationship_correction_events', 'verification_events', 'review_tasks',
        'hypotheses', 'hypothesis_derivation_sources', 'hypothesis_feedback_events',
        'submission_holds', 'submission_quarantine_payloads', 'submission_quarantine_tombstones', 'memory_placement_runs',
        'memory_placement_items', 'memory_dispute_sessions', 'relationship_conflict_cases',
        'relationship_conflict_positions', 'relationship_conflict_position_members',
        'relationship_conflict_events', 'relationship_conflict_review_runs',
        'relationship_conflict_derived_evidence_tasks', 'relationship_conflict_evidence_derivations',
        'relationship_conflict_resolution_plans', 'relationship_conflict_ai_assessment_attempts',
        'relationship_conflict_ai_assessment_events', 'search_documents', 'embedding_jobs',
        'community_snapshot_runs', 'community_records', 'community_memberships',
        'community_sources', 'community_summary_attempts', 'dream_cycle_runs',
        'dream_path_evaluations', 'recall_feedback_events'
    ];
BEGIN
    FOREACH target_table IN ARRAY target_tables LOOP
        IF to_regclass(format('public.%I', target_table)) IS NOT NULL THEN
            EXECUTE format('DROP POLICY IF EXISTS %I ON %I', 'dense_mem_20260820130004_generation_update', target_table);
            EXECUTE format('DROP POLICY IF EXISTS %I ON %I', 'dense_mem_20260820130004_generation_select', target_table);
        END IF;
    END LOOP;
END;
$dense_mem_space_generation_cleanup$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION dense_mem_note_private_content()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    previous_mode TEXT;
BEGIN
    IF NEW.space_id IS NULL THEN
        RETURN NEW;
    END IF;
    previous_mode := current_setting('app.tx_mode', true);
    PERFORM set_config('app.tx_mode', 'system', true);
    UPDATE memory_spaces
    SET private_content_at = GREATEST(COALESCE(private_content_at, NEW.created_at), NEW.created_at),
        updated_at = GREATEST(updated_at, NEW.created_at)
    WHERE id = NEW.space_id
      AND kind IN ('profile_private', 'credential_private');
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RETURN NEW;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RAISE;
END;
$$;

DROP TRIGGER IF EXISTS knowledge_ingests_private_content_at ON knowledge_ingests;
CREATE TRIGGER knowledge_ingests_private_content_at
AFTER INSERT ON knowledge_ingests
FOR EACH ROW EXECUTE FUNCTION dense_mem_note_private_content();

CREATE OR REPLACE FUNCTION dense_mem_ensure_private_space(p_team_id UUID, p_kind TEXT, p_owner_id UUID)
RETURNS UUID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    previous_mode TEXT;
    previous_team TEXT;
    result_id UUID;
BEGIN
    IF p_kind NOT IN ('profile_private', 'credential_private') OR p_owner_id IS NULL THEN
        RAISE EXCEPTION 'invalid private memory space request';
    END IF;
    previous_mode := current_setting('app.tx_mode', true);
    previous_team := current_setting('app.current_team_id', true);
    IF previous_mode NOT IN ('team', 'profile')
       OR NULLIF(previous_team, '')::uuid IS DISTINCT FROM p_team_id THEN
        RAISE EXCEPTION 'private memory space request is outside the authenticated team';
    END IF;

    PERFORM set_config('app.tx_mode', 'system', true);
    IF p_kind = 'profile_private' THEN
        INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
        VALUES (p_team_id, p_kind, p_owner_id)
        ON CONFLICT (team_id, owner_profile_id) WHERE kind = 'profile_private'
        DO UPDATE SET lifecycle_state = 'active', retired_at = NULL, updated_at = now()
        WHERE memory_spaces.lifecycle_state <> 'sealed'
        RETURNING id INTO result_id;
    ELSE
        INSERT INTO memory_spaces (team_id, kind, owner_credential_id)
        VALUES (p_team_id, p_kind, p_owner_id)
        ON CONFLICT (team_id, owner_credential_id) WHERE kind = 'credential_private'
        DO UPDATE SET lifecycle_state = 'active', retired_at = NULL, updated_at = now()
        WHERE memory_spaces.lifecycle_state <> 'sealed'
        RETURNING id INTO result_id;
    END IF;
    IF result_id IS NULL THEN
        RAISE EXCEPTION 'private memory space is sealed';
    END IF;
    PERFORM set_config('app.tx_mode', previous_mode, true);
    RETURN result_id;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RAISE;
END;
$$;

SELECT set_config('app.tx_mode', 'system', true);
INSERT INTO app_config (key, value)
VALUES ('PRIVATE_MEMORY_RETENTION_DAYS', '0')
ON CONFLICT (key) DO NOTHING;

SELECT set_config('app.private_erasure_space_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.tx_mode', 'system', true);

-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_log_memory_space_timestamp
    ON audit_log(memory_space_id, timestamp DESC) WHERE memory_space_id IS NOT NULL;

-- The audit association is a bounded, resumable backfill so large audit
-- tables do not hold one migration transaction or row-lock set for the
-- entire upgrade. Rows without a surviving credential and exact team match
-- remain unassociated and cannot be scrubbed by erasure.
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);

DROP POLICY IF EXISTS audit_log_memory_space_backfill_select ON audit_log;
DROP POLICY IF EXISTS audit_log_memory_space_backfill_update ON audit_log;
CREATE POLICY audit_log_memory_space_backfill_select ON audit_log
    FOR SELECT
    USING (current_setting('app.tx_mode', true) = 'migration');
CREATE POLICY audit_log_memory_space_backfill_update ON audit_log
    FOR UPDATE
    USING (current_setting('app.tx_mode', true) = 'migration')
    WITH CHECK (current_setting('app.tx_mode', true) = 'migration');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_private_memory_audit_20260820130004()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    updated_rows INTEGER;
BEGIN
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
        PERFORM set_config('app.allowed_space_ids', '', true);
        PERFORM set_config('app.private_erasure_space_id', '', true);

        WITH batch AS MATERIALIZED (
            SELECT audit.id, credential.memory_space_id
            FROM audit_log AS audit
            JOIN credentials AS credential
              ON credential.id = CASE
                  WHEN audit.entity_id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'
                      THEN audit.entity_id::uuid
                  ELSE NULL
              END
             AND credential.team_id = audit.team_id
            WHERE audit.memory_space_id IS NULL
              AND audit.entity_type = 'api_key'
              AND audit.entity_id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$'
              AND credential.memory_space_id IS NOT NULL
            ORDER BY audit.id
            LIMIT 500
            FOR UPDATE OF audit
        )
        UPDATE audit_log AS audit
        SET memory_space_id = batch.memory_space_id
        FROM batch
        WHERE audit.id = batch.id
          AND audit.memory_space_id IS NULL;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;
        COMMIT;
        EXIT WHEN updated_rows = 0;
    END LOOP;
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_private_memory_audit_20260820130004();
DROP PROCEDURE IF EXISTS dense_mem_backfill_private_memory_audit_20260820130004();

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);
DROP POLICY IF EXISTS audit_log_memory_space_backfill_update ON audit_log;
DROP POLICY IF EXISTS audit_log_memory_space_backfill_select ON audit_log;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $dense_mem_private_memory_down$
BEGIN
    RAISE EXCEPTION 'private-memory erasure rollback is prohibited; erased data requires an authorized backup restore';
END;
$dense_mem_private_memory_down$;

-- +goose StatementEnd
