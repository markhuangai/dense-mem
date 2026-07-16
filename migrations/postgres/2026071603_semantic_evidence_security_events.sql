-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS semantic_evidence_security_events (
    team_id UUID NOT NULL,
    security_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    fragment_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    event_kind TEXT NOT NULL,
    decision TEXT NOT NULL,
    scan_policy_hash TEXT NOT NULL DEFAULT '',
    actor_profile_id UUID NULL,
    reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, security_event_id),
    FOREIGN KEY (team_id, fragment_id) REFERENCES semantic_evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, actor_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT semantic_evidence_security_event_kind_check CHECK (event_kind IN ('deterministic_scan', 'reviewer_signal', 'verifier_signal', 'quarantine_release')),
    CONSTRAINT semantic_evidence_security_decision_check CHECK (decision IN ('pass', 'guarded', 'quarantine'))
);

CREATE INDEX IF NOT EXISTS semantic_evidence_security_events_fragment_idx
    ON semantic_evidence_security_events(team_id, fragment_id, created_at ASC, security_event_id ASC);

CREATE INDEX IF NOT EXISTS semantic_evidence_security_events_decision_idx
    ON semantic_evidence_security_events(team_id, decision, created_at DESC);

CREATE TABLE IF NOT EXISTS semantic_evidence_security_signals (
    team_id UUID NOT NULL,
    security_event_id UUID NOT NULL,
    signal_index INTEGER NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    span_start INTEGER NOT NULL,
    span_end INTEGER NOT NULL,
    quote TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, security_event_id, signal_index),
    FOREIGN KEY (team_id, security_event_id) REFERENCES semantic_evidence_security_events(team_id, security_event_id) ON DELETE CASCADE,
    CONSTRAINT semantic_evidence_security_signal_kind_check CHECK (kind IN (
        'role_control_spoofing',
        'instruction_override',
        'prompt_secret_extraction',
        'tool_exfiltration',
        'obfuscated_instruction',
        'hidden_control_markup'
    )),
    CONSTRAINT semantic_evidence_security_signal_severity_check CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    CONSTRAINT semantic_evidence_security_signal_span_check CHECK (span_start >= 0 AND span_end > span_start)
);

DROP TRIGGER IF EXISTS semantic_evidence_security_events_append_only ON semantic_evidence_security_events;
CREATE TRIGGER semantic_evidence_security_events_append_only
    BEFORE UPDATE OR DELETE ON semantic_evidence_security_events
    FOR EACH ROW
    EXECUTE FUNCTION prevent_semantic_append_only_mutation();

DROP TRIGGER IF EXISTS semantic_evidence_security_signals_append_only ON semantic_evidence_security_signals;
CREATE TRIGGER semantic_evidence_security_signals_append_only
    BEFORE UPDATE OR DELETE ON semantic_evidence_security_signals
    FOR EACH ROW
    EXECUTE FUNCTION prevent_semantic_append_only_mutation();

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'semantic_evidence_security_events',
        'semantic_evidence_security_signals'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_team_access', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR ALL USING (
                current_setting(''app.tx_mode'', true) = ''system''
                OR team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
            ) WITH CHECK (
                current_setting(''app.tx_mode'', true) = ''system''
                OR team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
            )',
            table_name || '_team_access',
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

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DROP TRIGGER IF EXISTS semantic_evidence_security_signals_append_only ON semantic_evidence_security_signals;
DROP TRIGGER IF EXISTS semantic_evidence_security_events_append_only ON semantic_evidence_security_events;
DROP TABLE IF EXISTS semantic_evidence_security_signals;
DROP TABLE IF EXISTS semantic_evidence_security_events;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
