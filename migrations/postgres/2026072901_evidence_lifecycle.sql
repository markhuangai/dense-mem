-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS evidence_lifecycle_operations (
    team_id UUID NOT NULL,
    lifecycle_operation_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    action TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    replacement_ingest_id UUID NULL,
    result JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, lifecycle_operation_id),
    CONSTRAINT evidence_lifecycle_operations_owner_ref_unique
        UNIQUE (team_id, lifecycle_operation_id, owner_profile_id),
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, replacement_ingest_id, owner_profile_id)
        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_lifecycle_operations_action_check
        CHECK (action IN ('supersede', 'retract')),
    CONSTRAINT evidence_lifecycle_operations_idempotency_nonempty
        CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT evidence_lifecycle_operations_request_hash_nonempty
        CHECK (btrim(request_hash) <> ''),
    CONSTRAINT evidence_lifecycle_operations_reason_length_check
        CHECK (char_length(reason) <= 1000),
    CONSTRAINT evidence_lifecycle_operations_result_object_check
        CHECK (jsonb_typeof(result) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS evidence_lifecycle_operations_idempotency_unique
    ON evidence_lifecycle_operations(team_id, owner_profile_id, idempotency_key);

CREATE TABLE IF NOT EXISTS evidence_lifecycle_events (
    team_id UUID NOT NULL,
    lifecycle_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    lifecycle_operation_id UUID NOT NULL,
    target_fragment_id UUID NOT NULL,
    replacement_fragment_id UUID NULL,
    owner_profile_id UUID NOT NULL,
    action TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, lifecycle_event_id),
    FOREIGN KEY (team_id, lifecycle_operation_id, owner_profile_id)
        REFERENCES evidence_lifecycle_operations(team_id, lifecycle_operation_id, owner_profile_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (team_id, target_fragment_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, replacement_fragment_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_lifecycle_events_action_check
        CHECK (action IN ('supersede', 'retract')),
    CONSTRAINT evidence_lifecycle_events_replacement_check
        CHECK (
            (action = 'supersede' AND replacement_fragment_id IS NOT NULL)
            OR (action = 'retract' AND replacement_fragment_id IS NULL)
        ),
    CONSTRAINT evidence_lifecycle_events_distinct_replacement_check
        CHECK (replacement_fragment_id IS NULL OR replacement_fragment_id <> target_fragment_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS evidence_lifecycle_events_terminal_target_unique
    ON evidence_lifecycle_events(team_id, target_fragment_id);
CREATE INDEX IF NOT EXISTS evidence_lifecycle_events_operation_idx
    ON evidence_lifecycle_events(team_id, lifecycle_operation_id, created_at ASC, lifecycle_event_id ASC);
CREATE INDEX IF NOT EXISTS evidence_lifecycle_events_replacement_idx
    ON evidence_lifecycle_events(team_id, replacement_fragment_id)
    WHERE replacement_fragment_id IS NOT NULL;

DROP TRIGGER IF EXISTS evidence_lifecycle_operations_append_only ON evidence_lifecycle_operations;
CREATE TRIGGER evidence_lifecycle_operations_append_only
    BEFORE UPDATE OR DELETE ON evidence_lifecycle_operations
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

DROP TRIGGER IF EXISTS evidence_lifecycle_events_append_only ON evidence_lifecycle_events;
CREATE TRIGGER evidence_lifecycle_events_append_only
    BEFORE UPDATE OR DELETE ON evidence_lifecycle_events
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE evidence_lifecycle_operations ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_lifecycle_operations FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_lifecycle_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_lifecycle_events FORCE ROW LEVEL SECURITY;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['evidence_lifecycle_operations', 'evidence_lifecycle_events']
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_select', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_insert', table_name);
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
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
            table_name
        );
    END LOOP;
END $$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM evidence_lifecycle_operations)
       OR EXISTS (SELECT 1 FROM evidence_lifecycle_events) THEN
        RAISE EXCEPTION 'cannot roll back 2026072901: evidence lifecycle history exists';
    END IF;
END $$;

DROP TABLE IF EXISTS evidence_lifecycle_events;
DROP TABLE IF EXISTS evidence_lifecycle_operations;

-- +goose StatementEnd
