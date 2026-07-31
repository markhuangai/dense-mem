-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - This migration requires an exclusive maintenance window. Adding and making
--   accepted_at non-null takes normal transactional table locks; its backfill
--   visits each existing conflict member once.
-- - It is intentionally atomic rather than online/resumable: a timeout or
--   failure rolls back the schema and backfill together. Size the maintenance
--   window from the target table's row count and lock budget before applying.
-- - RLS impact: new workflow tables preserve the existing team/profile policy;
--   normal application mutation paths remain the conflict-review worker.
-- - Rollback: the Down migration is intentionally blocked once audit lineage
--   may exist; restoring requires a database backup or a forward migration.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE team_profiles
    ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE team_profiles
    DROP CONSTRAINT IF EXISTS team_profiles_system_marker_check,
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_shape_check,
    DROP CONSTRAINT IF EXISTS team_profiles_auth_source_check;

ALTER TABLE team_profiles
    ADD CONSTRAINT team_profiles_auth_source_check
        CHECK (auth_source IN ('api_key', 'sso', 'system')),
    ADD CONSTRAINT team_profiles_system_marker_check
        CHECK ((auth_source = 'system') = is_system),
    ADD CONSTRAINT team_profiles_auth_source_shape_check
        CHECK (
            (
                auth_source = 'api_key'
                AND key_hash IS NOT NULL
                AND key_prefix IS NOT NULL
                AND sso_identity_id IS NULL
                AND sso_provider_id IS NULL
                AND NULLIF(sso_subject, '') IS NULL
                AND sso_entitlement_status = 'unlinked'
            )
            OR (
                auth_source = 'sso'
                AND key_hash IS NULL
                AND key_prefix IS NULL
                AND NULLIF(sso_subject, '') IS NOT NULL
                AND sso_entitlement_status IN ('active', 'denied', 'error')
                AND (
                    (sso_identity_id IS NOT NULL AND sso_provider_id IS NOT NULL)
                    OR (sso_identity_id IS NULL AND sso_provider_id IS NULL)
                )
            )
            OR (
                auth_source = 'system'
                AND key_hash IS NULL
                AND key_prefix IS NULL
                AND sso_identity_id IS NULL
                AND sso_provider_id IS NULL
                AND NULLIF(sso_subject, '') IS NULL
                AND sso_entitlement_status = 'unlinked'
                AND revoked_at IS NOT NULL
                AND is_system
            )
        );

CREATE UNIQUE INDEX IF NOT EXISTS team_profiles_system_team_unique
    ON team_profiles(team_id)
    WHERE is_system;

DROP POLICY IF EXISTS team_profiles_system_conflict_insert_access ON team_profiles;
CREATE POLICY team_profiles_system_conflict_insert_access ON team_profiles
	FOR INSERT
	TO PUBLIC
	WITH CHECK (
		(
			current_setting('app.tx_mode', true) = 'migration'
			OR (
				current_setting('app.tx_mode', true) = 'system'
				AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
			)
		)
		AND auth_source = 'system'
		AND is_system
		AND revoked_at IS NOT NULL
	);

DO $$
DECLARE
    team_record RECORD;
    has_system_profile BOOLEAN;
BEGIN
    FOR team_record IN SELECT id FROM teams LOOP
        SELECT EXISTS (
            SELECT 1
            FROM team_profiles
            WHERE team_id = team_record.id
              AND is_system
        ) INTO has_system_profile;
        CONTINUE WHEN has_system_profile;

        FOR attempt IN 1..5 LOOP
            INSERT INTO team_profiles (
                team_id, key_hash, key_prefix, key_suffix, name, scopes, role, rate_limit,
                revoked_at, auth_source, is_system
            ) VALUES (
                team_record.id, NULL, NULL, NULL,
                '__dense_mem_conflict_system__:' || gen_random_uuid()::text,
                ARRAY[]::text[], 'member', 0, now(), 'system', true
            )
            ON CONFLICT DO NOTHING;

            SELECT EXISTS (
                SELECT 1
                FROM team_profiles
                WHERE team_id = team_record.id
                  AND is_system
            ) INTO has_system_profile;
            EXIT WHEN has_system_profile;
        END LOOP;

        IF NOT has_system_profile THEN
            RAISE EXCEPTION 'could not create conflict system profile for team %', team_record.id;
        END IF;
    END LOOP;
END $$;

INSERT INTO semantic_team_refs (team_id)
SELECT id
FROM teams
ON CONFLICT (team_id) DO NOTHING;

INSERT INTO semantic_profile_refs (team_id, profile_id)
SELECT team_id, id
FROM team_profiles
WHERE is_system
ON CONFLICT (team_id, profile_id) DO NOTHING;

ALTER TABLE evidence_lifecycle_operations
    ADD COLUMN IF NOT EXISTS actor_profile_id UUID NULL;

ALTER TABLE evidence_lifecycle_operations
    DROP CONSTRAINT IF EXISTS evidence_lifecycle_operations_actor_profile_fk;

ALTER TABLE evidence_lifecycle_operations
    ADD CONSTRAINT evidence_lifecycle_operations_actor_profile_fk
    FOREIGN KEY (team_id, actor_profile_id)
    REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT;

ALTER TABLE relationship_conflict_position_members
    ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ NULL;

UPDATE relationship_conflict_position_members AS member
SET accepted_at = COALESCE(support.created_at, member.first_seen_at)
FROM relationship_evidence_supports AS support
WHERE support.team_id = member.team_id
  AND support.support_id = member.support_id
  AND member.accepted_at IS NULL;

UPDATE relationship_conflict_position_members
SET accepted_at = first_seen_at
WHERE accepted_at IS NULL;

ALTER TABLE relationship_conflict_position_members
    ALTER COLUMN accepted_at SET NOT NULL;

ALTER TABLE evidence_fragments
    DROP CONSTRAINT IF EXISTS evidence_fragments_authority_check;

ALTER TABLE evidence_fragments
    ADD CONSTRAINT evidence_fragments_authority_check
    CHECK (authority IN ('authoritative', 'primary', 'secondary', 'inferred', 'unknown'));

ALTER TABLE relationship_conflict_events
    DROP CONSTRAINT IF EXISTS relationship_conflict_events_action_check;

ALTER TABLE relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_action_check CHECK (action IN (
        'opened', 'position_added', 'member_added', 'evaluated', 'marked_overdue',
        'resolved', 'relationship_updated', 'dismissed', 'ai_assessment_reserved',
        'ai_assessed', 'resolution_pending', 'evidence_retracted',
        'derived_replacement_staged', 'derived_replacement_failed'
    ));

CREATE TABLE IF NOT EXISTS relationship_conflict_ai_assessment_attempts (
    team_id UUID NOT NULL,
    assessment_attempt_id UUID NOT NULL DEFAULT gen_random_uuid(),
    conflict_id UUID NOT NULL,
    case_version INTEGER NOT NULL,
    local_assessment_date DATE NOT NULL,
    model TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'reserved',
    selected_position_id UUID NULL,
    confidence DOUBLE PRECISION NULL,
    provider_turns INTEGER NOT NULL DEFAULT 0,
    response_hash TEXT NOT NULL DEFAULT '',
    failure_class TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, assessment_attempt_id),
    UNIQUE (team_id, conflict_id, case_version, local_assessment_date, model, policy_version),
    FOREIGN KEY (team_id, conflict_id) REFERENCES relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, selected_position_id) REFERENCES relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_ai_assessment_case_version_check CHECK (case_version >= 1),
    CONSTRAINT relationship_conflict_ai_assessment_model_nonempty CHECK (btrim(model) <> ''),
    CONSTRAINT relationship_conflict_ai_assessment_policy_nonempty CHECK (btrim(policy_version) <> ''),
    CONSTRAINT relationship_conflict_ai_assessment_status_check CHECK (status IN ('reserved', 'selected', 'abstained', 'failed', 'superseded')),
    CONSTRAINT relationship_conflict_ai_assessment_confidence_check CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    CONSTRAINT relationship_conflict_ai_assessment_provider_turns_check CHECK (provider_turns >= 0),
    CONSTRAINT relationship_conflict_ai_assessment_response_hash_length_check CHECK (char_length(response_hash) <= 128),
    CONSTRAINT relationship_conflict_ai_assessment_failure_class_length_check CHECK (char_length(failure_class) <= 128),
    CONSTRAINT relationship_conflict_ai_assessment_selected_shape_check CHECK (
        (status = 'selected' AND selected_position_id IS NOT NULL AND confidence IS NOT NULL)
        OR (status = 'abstained' AND selected_position_id IS NULL AND confidence = 0)
        OR (status IN ('reserved', 'failed', 'superseded') AND selected_position_id IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS relationship_conflict_ai_assessment_case_idx
    ON relationship_conflict_ai_assessment_attempts(team_id, conflict_id, case_version, created_at ASC);

CREATE INDEX IF NOT EXISTS relationship_conflict_ai_assessment_failure_count_idx
    ON relationship_conflict_ai_assessment_attempts(team_id, conflict_id, case_version, model, policy_version)
    WHERE status = 'failed';

CREATE TABLE IF NOT EXISTS relationship_conflict_ai_assessment_events (
    team_id UUID NOT NULL,
    assessment_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    assessment_attempt_id UUID NOT NULL,
    action TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, assessment_event_id),
    FOREIGN KEY (team_id, assessment_attempt_id)
        REFERENCES relationship_conflict_ai_assessment_attempts(team_id, assessment_attempt_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_ai_assessment_event_action_check CHECK (action IN ('reserved', 'selected', 'abstained', 'failed', 'superseded')),
    CONSTRAINT relationship_conflict_ai_assessment_event_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS relationship_conflict_ai_assessment_events_attempt_idx
    ON relationship_conflict_ai_assessment_events(team_id, assessment_attempt_id, created_at ASC, assessment_event_id ASC);

DROP TRIGGER IF EXISTS relationship_conflict_ai_assessment_events_append_only ON relationship_conflict_ai_assessment_events;
CREATE TRIGGER relationship_conflict_ai_assessment_events_append_only
    BEFORE UPDATE OR DELETE ON relationship_conflict_ai_assessment_events
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

CREATE TABLE IF NOT EXISTS relationship_conflict_resolution_plans (
    team_id UUID NOT NULL,
    resolution_plan_id UUID NOT NULL DEFAULT gen_random_uuid(),
    conflict_id UUID NOT NULL,
    expected_case_version INTEGER NOT NULL,
    preferred_position_id UUID NOT NULL,
    assessment_attempt_id UUID NULL,
    method TEXT NOT NULL,
    effective_at TIMESTAMPTZ NOT NULL,
    effective_time_basis TEXT NOT NULL DEFAULT 'recorded_at',
    status TEXT NOT NULL DEFAULT 'resolution_pending',
    failure_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, resolution_plan_id),
    UNIQUE (team_id, conflict_id, expected_case_version),
    FOREIGN KEY (team_id, conflict_id) REFERENCES relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, preferred_position_id) REFERENCES relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, assessment_attempt_id)
        REFERENCES relationship_conflict_ai_assessment_attempts(team_id, assessment_attempt_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_resolution_plans_version_check CHECK (expected_case_version >= 1),
    CONSTRAINT relationship_conflict_resolution_plans_method_check CHECK (method IN ('ai', 'last_write_wins')),
    CONSTRAINT relationship_conflict_resolution_plans_status_check CHECK (status IN ('resolution_pending', 'applied', 'superseded', 'failed')),
    CONSTRAINT relationship_conflict_resolution_plans_effective_basis_check CHECK (effective_time_basis IN ('valid_from', 'recorded_at'))
);

CREATE INDEX IF NOT EXISTS relationship_conflict_resolution_plans_pending_idx
    ON relationship_conflict_resolution_plans(team_id, status, created_at ASC)
    WHERE status = 'resolution_pending';

CREATE TABLE IF NOT EXISTS relationship_conflict_evidence_derivations (
    team_id UUID NOT NULL,
    derivation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    conflict_id UUID NOT NULL,
    target_fragment_id UUID NOT NULL,
    target_owner_profile_id UUID NOT NULL,
    selected_position_id UUID NOT NULL,
    replacement_fragment_id UUID NULL,
    system_profile_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, derivation_id),
    UNIQUE (team_id, conflict_id, target_fragment_id),
    FOREIGN KEY (team_id, conflict_id) REFERENCES relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, target_fragment_id, target_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, selected_position_id) REFERENCES relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, replacement_fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, system_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS relationship_conflict_evidence_derivations_conflict_idx
    ON relationship_conflict_evidence_derivations(team_id, conflict_id, created_at ASC);

DROP TRIGGER IF EXISTS relationship_conflict_evidence_derivations_append_only ON relationship_conflict_evidence_derivations;
CREATE TRIGGER relationship_conflict_evidence_derivations_append_only
    BEFORE UPDATE OR DELETE ON relationship_conflict_evidence_derivations
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

CREATE TABLE IF NOT EXISTS relationship_conflict_derived_evidence_tasks (
    team_id UUID NOT NULL,
    derived_evidence_task_id UUID NOT NULL DEFAULT gen_random_uuid(),
    resolution_plan_id UUID NOT NULL,
    conflict_id UUID NOT NULL,
    target_fragment_id UUID NOT NULL,
    target_owner_profile_id UUID NOT NULL,
    selected_position_id UUID NOT NULL,
    system_profile_id UUID NOT NULL,
    source_group_key TEXT NOT NULL,
    origin_evidence_index INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_worker_id TEXT NULL,
    lease_until TIMESTAMPTZ NULL,
    last_review_run_id UUID NULL,
    last_failure_class TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, derived_evidence_task_id),
    UNIQUE (team_id, conflict_id, target_fragment_id),
    FOREIGN KEY (team_id, resolution_plan_id)
        REFERENCES relationship_conflict_resolution_plans(team_id, resolution_plan_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, conflict_id) REFERENCES relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, target_fragment_id, target_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, selected_position_id) REFERENCES relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, system_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_derived_evidence_tasks_status_check CHECK (status IN ('pending', 'processing', 'completed')),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_attempts_check CHECK (attempts >= 0),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_source_group_check CHECK (btrim(source_group_key) <> ''),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_origin_index_check CHECK (origin_evidence_index >= 0),
    CONSTRAINT relationship_conflict_derived_evidence_tasks_failure_length_check CHECK (char_length(last_failure_class) <= 128)
);

CREATE INDEX IF NOT EXISTS relationship_conflict_derived_evidence_tasks_claim_idx
    ON relationship_conflict_derived_evidence_tasks(team_id, status, lease_until, created_at ASC, derived_evidence_task_id ASC)
    WHERE status IN ('pending', 'processing');

ALTER TABLE relationship_conflict_ai_assessment_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_ai_assessment_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_ai_assessment_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_ai_assessment_events FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_resolution_plans ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_resolution_plans FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_evidence_derivations ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_evidence_derivations FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_derived_evidence_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_derived_evidence_tasks FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'relationship_conflict_ai_assessment_attempts',
        'relationship_conflict_ai_assessment_events',
        'relationship_conflict_resolution_plans',
        'relationship_conflict_evidence_derivations',
        'relationship_conflict_derived_evidence_tasks'
    ]
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_select', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_insert', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_update', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_select', table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert', table_name
        );
        IF table_name NOT IN ('relationship_conflict_ai_assessment_events', 'relationship_conflict_evidence_derivations') THEN
            EXECUTE format(
                'CREATE POLICY %I ON %I FOR UPDATE USING (
                    current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                    OR (
                        current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                        AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    )
                ) WITH CHECK (
                    current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                    OR (
                        current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                        AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    )
                )',
                table_name || '_update', table_name
            );
        END IF;
    END LOOP;
END $$;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION '2026073101_v2_overdue_conflict_resolution is irreversible because it records conflict-review audit lineage';
END $$;

-- +goose StatementEnd
