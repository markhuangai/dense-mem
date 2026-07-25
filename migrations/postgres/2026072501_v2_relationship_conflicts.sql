-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
DECLARE
    duplicate_identities TEXT;
BEGIN
    SELECT string_agg(
        format(
            'team_id=%s owner_profile_id=%s subject=%s predicate=%s object_entity=%s object_value=%s polarity=%s valid_from=%s scope=%s rows=%s',
            team_id,
            owner_profile_id,
            subject_entity_id,
            predicate_key,
            COALESCE(object_entity_id::text, ''),
            COALESCE(object_value_id::text, ''),
            polarity,
            COALESCE(valid_from::text, ''),
            COALESCE(scope_key, ''),
            row_count
        ),
        '; '
        ORDER BY team_id, owner_profile_id, subject_entity_id, predicate_key
    )
    INTO duplicate_identities
    FROM (
        SELECT team_id,
               owner_profile_id,
               subject_entity_id,
               predicate_key,
               object_entity_id,
               object_value_id,
               polarity,
               valid_from,
               scope_key,
               count(*) AS row_count
        FROM relationship_records
        GROUP BY team_id, owner_profile_id, subject_entity_id, predicate_key,
                 object_entity_id, object_value_id, polarity, valid_from, scope_key
        HAVING count(*) > 1
    ) AS duplicates;

    IF duplicate_identities IS NOT NULL THEN
        RAISE EXCEPTION 'cannot remove relationship_records.valid_to from identity: %', duplicate_identities;
    END IF;
END $$;

DROP INDEX IF EXISTS relationship_records_active_one_current_unique;
ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_identity_unique;
CREATE UNIQUE INDEX IF NOT EXISTS relationship_records_identity_unique_without_valid_to_idx
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        object_entity_id, object_value_id, polarity, valid_from, scope_key
    )
    NULLS NOT DISTINCT;
ALTER TABLE relationship_records
    ADD CONSTRAINT relationship_records_identity_unique
    UNIQUE USING INDEX relationship_records_identity_unique_without_valid_to_idx;

CREATE UNIQUE INDEX IF NOT EXISTS relationship_records_active_one_current_unique
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, scope_key
    )
    NULLS NOT DISTINCT
    WHERE current_cardinality = 'one'
      AND status = 'active'
      AND tier IN ('validated_claim', 'fact');

CREATE TABLE IF NOT EXISTS relationship_conflict_cases (
    team_id UUID NOT NULL,
    conflict_id UUID NOT NULL DEFAULT gen_random_uuid(),
    semantic_scope_key TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'cross_profile_current_state',
    status TEXT NOT NULL DEFAULT 'open',
    subject_entity_id UUID NOT NULL,
    predicate_key TEXT NOT NULL,
    predicate_version INTEGER NOT NULL DEFAULT 1,
    relationship_kind TEXT NOT NULL,
    current_cardinality TEXT NOT NULL,
    polarity TEXT NOT NULL DEFAULT '+',
    scope_key TEXT NULL,
    question TEXT NOT NULL DEFAULT '',
    policy_version TEXT NOT NULL DEFAULT 'cross_profile_conflict_v1',
    review_due_at TIMESTAMPTZ NOT NULL,
    next_review_at TIMESTAMPTZ NOT NULL,
    review_ttl_days INTEGER NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'Local',
    preferred_position_id UUID NULL,
    resolved_at TIMESTAMPTZ NULL,
    effective_at TIMESTAMPTZ NULL,
    effective_time_basis TEXT NOT NULL DEFAULT '',
    resolution_reason TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_worker_id TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ NULL,
    last_error TEXT NOT NULL DEFAULT '',
    last_review_run_id UUID NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, conflict_id),
    FOREIGN KEY (team_id, subject_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, predicate_key, predicate_version)
        REFERENCES team_predicate_definitions(team_id, predicate_key, version) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_cases_scope_nonempty CHECK (btrim(semantic_scope_key) <> ''),
    CONSTRAINT relationship_conflict_cases_kind_check CHECK (kind IN ('cross_profile_current_state')),
    CONSTRAINT relationship_conflict_cases_status_check CHECK (status IN ('open', 'overdue', 'resolved', 'dismissed')),
    CONSTRAINT relationship_conflict_cases_relationship_kind_check CHECK (relationship_kind IN ('state', 'event')),
    CONSTRAINT relationship_conflict_cases_cardinality_check CHECK (current_cardinality IN ('one', 'many')),
    CONSTRAINT relationship_conflict_cases_polarity_check CHECK (polarity IN ('+', '-')),
    CONSTRAINT relationship_conflict_cases_version_check CHECK (version >= 1),
    CONSTRAINT relationship_conflict_cases_attempts_check CHECK (attempts >= 0),
    CONSTRAINT relationship_conflict_cases_review_ttl_check CHECK (review_ttl_days BETWEEN 1 AND 30),
    CONSTRAINT relationship_conflict_cases_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS relationship_conflict_cases_open_scope_unique
    ON relationship_conflict_cases(team_id, semantic_scope_key)
    WHERE status IN ('open', 'overdue');
CREATE INDEX IF NOT EXISTS relationship_conflict_cases_due_idx
    ON relationship_conflict_cases(team_id, next_review_at, conflict_id)
    WHERE status IN ('open', 'overdue');
CREATE INDEX IF NOT EXISTS relationship_conflict_cases_lease_idx
    ON relationship_conflict_cases(team_id, lease_until)
    WHERE status IN ('open', 'overdue') AND lease_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS relationship_conflict_cases_subject_idx
    ON relationship_conflict_cases(team_id, subject_entity_id, predicate_key)
    WHERE status IN ('open', 'overdue', 'resolved');

CREATE TABLE IF NOT EXISTS relationship_conflict_positions (
    team_id UUID NOT NULL,
    conflict_id UUID NOT NULL,
    position_id UUID NOT NULL DEFAULT gen_random_uuid(),
    position_key TEXT NOT NULL,
    object_entity_id UUID NULL,
    object_value_id UUID NULL,
    disposition TEXT NOT NULL DEFAULT 'candidate',
    support_group_count INTEGER NOT NULL DEFAULT 0,
    authoritative_group_count INTEGER NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, position_id),
    UNIQUE (team_id, conflict_id, position_key),
    FOREIGN KEY (team_id, conflict_id) REFERENCES relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_entity_id) REFERENCES entity_records(team_id, entity_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, object_value_id) REFERENCES value_records(team_id, value_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_positions_object_check CHECK ((object_entity_id IS NULL) <> (object_value_id IS NULL)),
    CONSTRAINT relationship_conflict_positions_key_nonempty CHECK (btrim(position_key) <> ''),
    CONSTRAINT relationship_conflict_positions_disposition_check CHECK (disposition IN ('candidate', 'preferred', 'suppressed_current')),
    CONSTRAINT relationship_conflict_positions_counts_check CHECK (support_group_count >= 0 AND authoritative_group_count >= 0),
    CONSTRAINT relationship_conflict_positions_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS relationship_conflict_positions_case_idx
    ON relationship_conflict_positions(team_id, conflict_id, disposition, position_id);

CREATE TABLE IF NOT EXISTS relationship_conflict_position_members (
    team_id UUID NOT NULL,
    conflict_id UUID NOT NULL,
    position_id UUID NOT NULL,
    relationship_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    support_id UUID NULL,
    verification_event_id UUID NULL,
    fragment_id UUID NULL,
    source_group_key TEXT NOT NULL,
    authority TEXT NOT NULL DEFAULT 'primary',
    effective_at TIMESTAMPTZ NULL,
    effective_time_basis TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (team_id, position_id, relationship_id),
    FOREIGN KEY (team_id, conflict_id) REFERENCES relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, position_id) REFERENCES relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, support_id, owner_profile_id)
        REFERENCES relationship_evidence_supports(team_id, support_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, verification_event_id, owner_profile_id)
        REFERENCES verification_events(team_id, verification_event_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_members_source_group_nonempty CHECK (btrim(source_group_key) <> ''),
    CONSTRAINT relationship_conflict_members_authority_check CHECK (authority IN ('authoritative', 'primary', 'secondary', 'inferred', 'unknown')),
    CONSTRAINT relationship_conflict_members_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS relationship_conflict_members_case_idx
    ON relationship_conflict_position_members(team_id, conflict_id, relationship_id);
CREATE INDEX IF NOT EXISTS relationship_conflict_members_relationship_idx
    ON relationship_conflict_position_members(team_id, relationship_id);

CREATE TABLE IF NOT EXISTS relationship_conflict_events (
    team_id UUID NOT NULL,
    conflict_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    conflict_id UUID NOT NULL,
    position_id UUID NULL,
    relationship_id UUID NULL,
    owner_profile_id UUID NULL,
    action TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT '',
    actor_kind TEXT NOT NULL DEFAULT 'system',
    actor_profile_id UUID NULL,
    policy_version TEXT NOT NULL DEFAULT 'cross_profile_conflict_v1',
    idempotency_key TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, conflict_event_id),
    FOREIGN KEY (team_id, conflict_id) REFERENCES relationship_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, position_id) REFERENCES relationship_conflict_positions(team_id, position_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, relationship_id, owner_profile_id)
        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, actor_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_events_action_check CHECK (action IN (
        'opened', 'position_added', 'member_added', 'evaluated', 'marked_overdue',
        'resolved', 'relationship_updated'
    )),
    CONSTRAINT relationship_conflict_events_actor_check CHECK (actor_kind IN ('system', 'profile')),
    CONSTRAINT relationship_conflict_events_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS relationship_conflict_events_idempotency_unique
    ON relationship_conflict_events(team_id, idempotency_key)
    WHERE idempotency_key <> '';
CREATE INDEX IF NOT EXISTS relationship_conflict_events_case_idx
    ON relationship_conflict_events(team_id, conflict_id, created_at DESC, conflict_event_id DESC);

CREATE TABLE IF NOT EXISTS relationship_conflict_review_runs (
    team_id UUID NOT NULL,
    review_run_id UUID NOT NULL DEFAULT gen_random_uuid(),
    local_run_date DATE NOT NULL,
    policy_version TEXT NOT NULL DEFAULT 'cross_profile_conflict_v1',
    status TEXT NOT NULL DEFAULT 'reserved',
    worker_id TEXT NOT NULL DEFAULT '',
    timezone TEXT NOT NULL DEFAULT 'Local',
    lease_until TIMESTAMPTZ NULL,
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    claimed_cases INTEGER NOT NULL DEFAULT 0,
    resolved_cases INTEGER NOT NULL DEFAULT 0,
    overdue_cases INTEGER NOT NULL DEFAULT 0,
    no_op_cases INTEGER NOT NULL DEFAULT 0,
    failed_cases INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, review_run_id),
    UNIQUE (team_id, local_run_date, policy_version),
    FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE RESTRICT,
    CONSTRAINT relationship_conflict_review_runs_status_check CHECK (status IN ('reserved', 'running', 'completed', 'failed')),
    CONSTRAINT relationship_conflict_review_runs_counts_check CHECK (
        claimed_cases >= 0 AND resolved_cases >= 0 AND overdue_cases >= 0
        AND no_op_cases >= 0 AND failed_cases >= 0
    ),
    CONSTRAINT relationship_conflict_review_runs_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS relationship_conflict_review_runs_status_idx
    ON relationship_conflict_review_runs(team_id, status, lease_until);

DROP TRIGGER IF EXISTS relationship_conflict_events_append_only ON relationship_conflict_events;
CREATE TRIGGER relationship_conflict_events_append_only
    BEFORE UPDATE OR DELETE ON relationship_conflict_events
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

ALTER TABLE relationship_conflict_cases ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_cases FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_positions ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_positions FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_position_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_position_members FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_events FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_review_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_conflict_review_runs FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'relationship_conflict_cases',
        'relationship_conflict_positions',
        'relationship_conflict_position_members',
        'relationship_conflict_events',
        'relationship_conflict_review_runs'
    ]
    LOOP
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_select',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
            table_name
        );
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
            table_name || '_update',
            table_name
        );
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

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP TABLE IF EXISTS relationship_conflict_review_runs;
DROP TRIGGER IF EXISTS relationship_conflict_events_append_only ON relationship_conflict_events;
DROP TABLE IF EXISTS relationship_conflict_events;
DROP TABLE IF EXISTS relationship_conflict_position_members;
DROP TABLE IF EXISTS relationship_conflict_positions;
DROP TABLE IF EXISTS relationship_conflict_cases;

DO $$
DECLARE
    duplicate_identities TEXT;
BEGIN
    SELECT string_agg(
        format(
            'team_id=%s owner_profile_id=%s subject=%s predicate=%s object_entity=%s object_value=%s polarity=%s valid_from=%s valid_to=%s scope=%s rows=%s',
            team_id,
            owner_profile_id,
            subject_entity_id,
            predicate_key,
            COALESCE(object_entity_id::text, ''),
            COALESCE(object_value_id::text, ''),
            polarity,
            COALESCE(valid_from::text, ''),
            COALESCE(valid_to::text, ''),
            COALESCE(scope_key, ''),
            row_count
        ),
        '; '
        ORDER BY team_id, owner_profile_id, subject_entity_id, predicate_key
    )
    INTO duplicate_identities
    FROM (
        SELECT team_id,
               owner_profile_id,
               subject_entity_id,
               predicate_key,
               object_entity_id,
               object_value_id,
               polarity,
               valid_from,
               valid_to,
               scope_key,
               count(*) AS row_count
        FROM relationship_records
        GROUP BY team_id, owner_profile_id, subject_entity_id, predicate_key,
                 object_entity_id, object_value_id, polarity, valid_from, valid_to, scope_key
        HAVING count(*) > 1
    ) AS duplicates;

    IF duplicate_identities IS NOT NULL THEN
        RAISE EXCEPTION 'cannot restore relationship_records.valid_to identity: %', duplicate_identities;
    END IF;
END $$;

DROP INDEX IF EXISTS relationship_records_active_one_current_unique;
ALTER TABLE relationship_records
    DROP CONSTRAINT IF EXISTS relationship_records_identity_unique;
CREATE UNIQUE INDEX IF NOT EXISTS relationship_records_identity_unique_with_valid_to_idx
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        object_entity_id, object_value_id, polarity, valid_from, valid_to, scope_key
    )
    NULLS NOT DISTINCT;
ALTER TABLE relationship_records
    ADD CONSTRAINT relationship_records_identity_unique
    UNIQUE USING INDEX relationship_records_identity_unique_with_valid_to_idx;

CREATE UNIQUE INDEX IF NOT EXISTS relationship_records_active_one_current_unique
    ON relationship_records (
        team_id, owner_profile_id, subject_entity_id, predicate_key,
        polarity, valid_from, valid_to, scope_key
    )
    NULLS NOT DISTINCT
    WHERE current_cardinality = 'one'
      AND status = 'active'
      AND tier IN ('validated_claim', 'fact');

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
