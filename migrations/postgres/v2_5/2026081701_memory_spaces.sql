-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: catalog creation is small; memory-owned tables receive an
-- additive nullable UUID and an index. The bounded backfill only updates rows
-- whose space is null and can be resumed safely by rerunning this migration.
-- RLS impact: space visibility is an additional transaction-local predicate;
-- migration/system modes remain explicit and are never request selectable.
-- Backfill: existing rows and old-writer inserts map to the team's shared row.
-- Backward compatibility: old binaries may omit space_id while this migration
-- is rolling out; the default/trigger fills team_shared until the writer barrier.
-- Rollback: application rollback retains the catalog and shared placement. The
-- down migration is intentionally blocked because removing the boundary would
-- make private data ambiguous.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

CREATE TABLE IF NOT EXISTS memory_spaces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('team_shared', 'profile_private', 'credential_private')),
    owner_profile_id UUID NULL,
    owner_credential_id UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT memory_spaces_owner_shape CHECK (
        (kind = 'team_shared' AND owner_profile_id IS NULL AND owner_credential_id IS NULL)
        OR (kind = 'profile_private' AND owner_profile_id IS NOT NULL AND owner_credential_id IS NULL)
        OR (kind = 'credential_private' AND owner_profile_id IS NULL AND owner_credential_id IS NOT NULL)
    ),
    UNIQUE (team_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS memory_spaces_team_shared_unique
    ON memory_spaces(team_id) WHERE kind = 'team_shared';
CREATE UNIQUE INDEX IF NOT EXISTS memory_spaces_profile_private_unique
    ON memory_spaces(team_id, owner_profile_id) WHERE kind = 'profile_private';
CREATE UNIQUE INDEX IF NOT EXISTS memory_spaces_credential_private_unique
    ON memory_spaces(team_id, owner_credential_id) WHERE kind = 'credential_private';
CREATE INDEX IF NOT EXISTS memory_spaces_team_kind_idx ON memory_spaces(team_id, kind, id);

CREATE OR REPLACE FUNCTION dense_mem_team_shared_space(p_team_id UUID)
RETURNS UUID
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    space_id UUID;
BEGIN
    SELECT id INTO space_id FROM memory_spaces
    WHERE team_id = p_team_id AND kind = 'team_shared'
    LIMIT 1;
    IF space_id IS NULL THEN
        RAISE EXCEPTION 'team shared memory space is not initialized for team %', p_team_id;
    END IF;
    RETURN space_id;
END;
$$;

INSERT INTO memory_spaces (team_id, kind)
SELECT id, 'team_shared'
FROM teams
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION dense_mem_initialize_team_shared_space()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    previous_mode TEXT;
    space_matches BOOLEAN;
BEGIN
    INSERT INTO memory_spaces (team_id, kind)
    VALUES (NEW.id, 'team_shared')
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS teams_memory_space_defaults ON teams;
CREATE TRIGGER teams_memory_space_defaults
AFTER INSERT ON teams
FOR EACH ROW EXECUTE FUNCTION dense_mem_initialize_team_shared_space();

-- SSO membership spaces are created up front. A later credential binding may
-- select one of these rows, while legacy credentials remain team-shared.
INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
SELECT membership.team_id, 'profile_private', membership.actor_identity_id
FROM team_memberships AS membership
WHERE membership.status = 'active'
  AND membership.sso_provider_id IS NOT NULL
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION dense_mem_initialize_profile_private_space()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    IF NEW.status = 'active' AND NEW.sso_provider_id IS NOT NULL THEN
        INSERT INTO memory_spaces (team_id, kind, owner_profile_id)
        VALUES (NEW.team_id, 'profile_private', NEW.actor_identity_id)
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS team_memberships_memory_space_defaults ON team_memberships;
CREATE TRIGGER team_memberships_memory_space_defaults
AFTER INSERT OR UPDATE OF actor_identity_id, team_id, status, sso_provider_id ON team_memberships
FOR EACH ROW EXECUTE FUNCTION dense_mem_initialize_profile_private_space();

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
        DO UPDATE SET updated_at = now()
        RETURNING id INTO result_id;
    ELSE
        INSERT INTO memory_spaces (team_id, kind, owner_credential_id)
        VALUES (p_team_id, p_kind, p_owner_id)
        ON CONFLICT (team_id, owner_credential_id) WHERE kind = 'credential_private'
        DO UPDATE SET updated_at = now()
        RETURNING id INTO result_id;
    END IF;
    PERFORM set_config('app.tx_mode', previous_mode, true);
    RETURN result_id;
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.tx_mode', COALESCE(previous_mode, ''), true);
    RAISE;
END;
$$;

ALTER TABLE credentials ADD COLUMN IF NOT EXISTS memory_binding TEXT NOT NULL DEFAULT 'shared_only';
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS memory_space_id UUID NULL;
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_memory_binding_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_memory_binding_check
    CHECK (memory_binding IN ('shared_only', 'profile_private', 'credential_private'));
-- Keep the established one-active-personal-key invariant for SSO identities.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credentials_owner_team_active_unique
    ON credentials(owner_identity_id, team_id)
    WHERE owner_identity_id IS NOT NULL AND kind = 'api_key' AND status = 'active';

UPDATE credentials AS credential
SET memory_space_id = shared.id
FROM memory_spaces AS shared
WHERE shared.team_id = credential.team_id
  AND shared.kind = 'team_shared'
  AND credential.memory_space_id IS NULL;

ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_team_memory_space_fk;
ALTER TABLE credentials ADD CONSTRAINT credentials_team_memory_space_fk
    FOREIGN KEY (team_id, memory_space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS credentials_team_memory_binding_idx
    ON credentials(team_id, memory_binding, memory_space_id);

CREATE OR REPLACE FUNCTION dense_mem_credentials_space_defaults()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    previous_mode TEXT;
    space_matches BOOLEAN;
BEGIN
    IF TG_OP = 'UPDATE' AND (
        OLD.memory_binding IS DISTINCT FROM NEW.memory_binding OR
        OLD.memory_space_id IS DISTINCT FROM NEW.memory_space_id
    ) THEN
        RAISE EXCEPTION 'credential memory binding is immutable';
    END IF;
    IF NEW.memory_binding IS NULL OR NEW.memory_binding = '' THEN
        NEW.memory_binding := 'shared_only';
    END IF;
    IF NEW.memory_space_id IS NULL THEN
        NEW.memory_space_id := dense_mem_team_shared_space(NEW.team_id);
    END IF;
    IF NEW.memory_binding = 'shared_only' THEN
        NEW.memory_space_id := dense_mem_team_shared_space(NEW.team_id);
    END IF;
    previous_mode := current_setting('app.tx_mode', true);
    PERFORM set_config('app.tx_mode', 'system', true);
    SELECT EXISTS (
        SELECT 1
        FROM memory_spaces
        WHERE id = NEW.memory_space_id
          AND team_id = NEW.team_id
          AND kind = CASE NEW.memory_binding
              WHEN 'profile_private' THEN 'profile_private'
              WHEN 'credential_private' THEN 'credential_private'
              ELSE 'team_shared'
          END
    ) INTO space_matches;
    PERFORM set_config('app.tx_mode', previous_mode, true);
    IF NOT space_matches THEN
        RAISE EXCEPTION 'credential memory space does not match memory binding';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS credentials_memory_space_defaults ON credentials;
CREATE TRIGGER credentials_memory_space_defaults
BEFORE INSERT OR UPDATE OF team_id, memory_binding, memory_space_id ON credentials
FOR EACH ROW EXECUTE FUNCTION dense_mem_credentials_space_defaults();

CREATE OR REPLACE FUNCTION dense_mem_memory_space_defaults()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    IF NEW.space_id IS NULL AND NEW.team_id IS NOT NULL THEN
        NEW.space_id := dense_mem_team_shared_space(NEW.team_id);
    END IF;
    RETURN NEW;
END;
$$;

DO $dense_mem_space_columns$
DECLARE
    target_table TEXT;
    target_tables CONSTANT TEXT[] := ARRAY[
        'knowledge_ingests', 'evidence_sources', 'evidence_source_revisions',
        'evidence_fragments', 'evidence_security_events', 'evidence_security_signals',
        'evidence_quarantines', 'evidence_lifecycle_operations', 'evidence_lifecycle_events',
        'placement_runs', 'placement_items', 'placement_outcomes', 'placement_assessments',
        'entity_records', 'entity_names', 'entity_resolution_events',
        'entity_correction_plans', 'entity_correction_events', 'value_records',
        'relationship_records', 'relationship_observations', 'relationship_evidence_supports',
        'relationship_support_decision_events', 'relationship_transition_events',
        'relationship_cross_references', 'relationship_correction_submissions',
        'relationship_correction_events', 'verification_events', 'review_tasks',
        'hypotheses', 'hypothesis_derivation_sources', 'hypothesis_feedback_events',
        'submission_holds', 'submission_quarantine_payloads', 'memory_placement_runs',
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
    has_team BOOLEAN;
    has_space BOOLEAN;
    is_required BOOLEAN;
BEGIN
    FOREACH target_table IN ARRAY target_tables LOOP
        SELECT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND information_schema.columns.table_name = target_table AND column_name = 'team_id'
        ) INTO has_team;
        IF NOT has_team THEN
            CONTINUE;
        END IF;
        SELECT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_schema = 'public' AND information_schema.columns.table_name = target_table AND column_name = 'space_id'
        ) INTO has_space;
        IF NOT has_space THEN
            EXECUTE format('ALTER TABLE %I ADD COLUMN space_id UUID NULL', target_table);
        END IF;
        EXECUTE format('UPDATE %I AS row SET space_id = dense_mem_team_shared_space(row.team_id) WHERE row.team_id IS NOT NULL AND row.space_id IS NULL', target_table);
        EXECUTE format('CREATE INDEX IF NOT EXISTS %I ON %I(team_id, space_id)', target_table || '_team_space_idx', target_table);
        IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = target_table || '_memory_space_defaults') THEN
            EXECUTE format('CREATE TRIGGER %I BEFORE INSERT OR UPDATE OF team_id, space_id ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_memory_space_defaults()', target_table || '_memory_space_defaults', target_table);
        END IF;
        SELECT is_nullable = 'NO' INTO is_required
        FROM information_schema.columns
        WHERE table_schema = 'public' AND information_schema.columns.table_name = target_table AND column_name = 'team_id';
        IF is_required THEN
            EXECUTE format('ALTER TABLE %I ALTER COLUMN space_id SET NOT NULL', target_table);
        END IF;
        IF NOT EXISTS (
            SELECT 1
            FROM pg_constraint AS constraint_row
            JOIN pg_class AS relation ON relation.oid = constraint_row.conrelid
            JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
            WHERE namespace.nspname = 'public'
              AND relation.relname = target_table
              AND constraint_row.conname = target_table || '_memory_space_fk'
        ) THEN
            EXECUTE format(
                'ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT',
                target_table,
                target_table || '_memory_space_fk'
            );
        END IF;
    END LOOP;
END;
$dense_mem_space_columns$;

CREATE OR REPLACE FUNCTION dense_mem_space_allowed(candidate UUID)
RETURNS BOOLEAN
LANGUAGE SQL
STABLE
AS $$
    SELECT current_setting('app.tx_mode', true) IN ('system', 'migration')
       OR candidate::text = ANY(string_to_array(COALESCE(current_setting('app.allowed_space_ids', true), ''), ','));
$$;

ALTER TABLE memory_spaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_spaces FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS memory_spaces_select ON memory_spaces;
CREATE POLICY memory_spaces_select ON memory_spaces FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        AND (kind = 'team_shared' OR dense_mem_space_allowed(id)))
);
DROP POLICY IF EXISTS memory_spaces_write ON memory_spaces;
CREATE POLICY memory_spaces_write ON memory_spaces FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
);
DROP POLICY IF EXISTS memory_spaces_update ON memory_spaces;
CREATE POLICY memory_spaces_update ON memory_spaces FOR UPDATE
USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));
DROP POLICY IF EXISTS memory_spaces_delete ON memory_spaces;
CREATE POLICY memory_spaces_delete ON memory_spaces FOR DELETE
USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));

SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.tx_mode', 'system', true);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_memory_spaces_down$
BEGIN
    RAISE EXCEPTION 'memory-space boundary rollback is prohibited; restore the pre-migration backup';
END;
$dense_mem_memory_spaces_down$;
-- +goose StatementEnd
