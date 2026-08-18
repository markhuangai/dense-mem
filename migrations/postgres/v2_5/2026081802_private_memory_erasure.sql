-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: memory_spaces and every cataloged space-owned table
-- receive additive metadata columns without rewriting historical rows. New
-- constraints are installed NOT VALID, and DDL locks are held only while each
-- column, constraint, policy, and trigger is installed.
-- RLS impact: erasure control tables are FORCE RLS and system-only. Canonical
-- and derived rows keep their existing policies; deletion runs as system mode
-- with an exact transaction-local private-space fence.
-- Backfill: historical generation and private-content age remain null. New or
-- updated rows receive the active generation from the write fence; retention
-- derives legacy private-content age later in bounded, resumable batches.
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

CREATE TABLE private_memory_legal_holds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL,
    space_id UUID NOT NULL,
    reason_code TEXT NOT NULL CHECK (reason_code ~ '^[a-z0-9][a-z0-9_.-]{0,63}$'),
    actor_class TEXT NOT NULL CHECK (actor_class IN ('control')),
    placed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ NULL,
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX private_memory_legal_holds_active_unique
    ON private_memory_legal_holds(space_id) WHERE released_at IS NULL;
CREATE INDEX private_memory_legal_holds_team_space_idx
    ON private_memory_legal_holds(team_id, space_id, placed_at DESC);

CREATE TABLE private_memory_erasure_operations (
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
CREATE UNIQUE INDEX private_memory_erasure_idempotency_unique
    ON private_memory_erasure_operations(idempotency_scope_hash);
CREATE INDEX private_memory_erasure_claim_idx
    ON private_memory_erasure_operations(status, next_attempt_at, lease_until, requested_at, id)
    WHERE status IN ('queued', 'processing');
CREATE UNIQUE INDEX private_memory_erasure_space_active_unique
    ON private_memory_erasure_operations(space_id)
    WHERE space_id IS NOT NULL AND status IN ('queued', 'processing');

CREATE TABLE private_memory_retention_runs (
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
CREATE UNIQUE INDEX private_memory_retention_runs_idempotency_unique
    ON private_memory_retention_runs(idempotency_scope_hash);
CREATE INDEX private_memory_retention_runs_started_idx
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
CREATE INDEX IF NOT EXISTS idx_audit_log_memory_space_timestamp
    ON audit_log(memory_space_id, timestamp DESC) WHERE memory_space_id IS NOT NULL;
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
       AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
           = NULLIF(to_jsonb(OLD)->>'space_id', '')::uuid THEN
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
    IF NEW.space_generation IS NULL THEN
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

DO $dense_mem_space_generation$
DECLARE
    target_table TEXT;
    delete_policy_name TEXT;
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
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR DELETE USING (current_setting(''app.tx_mode'', true) = ''system'' AND space_id = NULLIF(current_setting(''app.private_erasure_space_id'', true), '''')::uuid)',
            delete_policy_name,
            target_table
        );
    END LOOP;
END;
$dense_mem_space_generation$;

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

-- +goose Down
-- +goose StatementBegin

DO $dense_mem_private_memory_down$
BEGIN
    RAISE EXCEPTION 'private-memory erasure rollback is prohibited; erased data requires an authorized backup restore';
END;
$dense_mem_private_memory_down$;

-- +goose StatementEnd
