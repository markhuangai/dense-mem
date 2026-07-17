-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE UNIQUE INDEX IF NOT EXISTS idx_team_profiles_team_id_id_unique
    ON team_profiles(team_id, id);

CREATE OR REPLACE FUNCTION prevent_v2_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS semantic_team_refs (
    team_id UUID NOT NULL PRIMARY KEY REFERENCES teams(id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS semantic_profile_refs (
    team_id UUID NOT NULL,
    profile_id UUID NOT NULL,
    PRIMARY KEY (team_id, profile_id),
    FOREIGN KEY (team_id) REFERENCES semantic_team_refs(team_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, profile_id) REFERENCES team_profiles(team_id, id) ON DELETE RESTRICT
);

INSERT INTO semantic_team_refs (team_id)
SELECT id FROM teams
ON CONFLICT (team_id) DO NOTHING;

INSERT INTO semantic_profile_refs (team_id, profile_id)
SELECT team_id, id FROM team_profiles
ON CONFLICT (team_id, profile_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS knowledge_ingests (
    team_id UUID NOT NULL,
    ingest_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    request_hash TEXT NOT NULL DEFAULT '',
    source_summary TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued',
    proposal JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, ingest_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT knowledge_ingests_status_check
        CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'failed')),
    CONSTRAINT knowledge_ingests_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT knowledge_ingests_proposal_object_check CHECK (jsonb_typeof(proposal) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS knowledge_ingests_idempotency_unique
    ON knowledge_ingests(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS knowledge_ingests_team_status_created_idx
    ON knowledge_ingests(team_id, status, created_at ASC, ingest_id);

CREATE TABLE IF NOT EXISTS evidence_sources (
    team_id UUID NOT NULL,
    source_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    source_key TEXT NOT NULL,
    source_kind TEXT NOT NULL DEFAULT 'conversation',
    authority TEXT NOT NULL DEFAULT 'primary',
    current_revision_id UUID NULL,
    current_revision_token TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, source_id),
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_sources_key_nonempty CHECK (btrim(source_key) <> ''),
    CONSTRAINT evidence_sources_kind_check CHECK (source_kind IN ('conversation', 'document', 'integration', 'manual')),
    CONSTRAINT evidence_sources_authority_check CHECK (authority IN ('primary', 'secondary', 'derived')),
    CONSTRAINT evidence_sources_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS evidence_sources_owner_key_unique
    ON evidence_sources(team_id, owner_profile_id, source_key);

CREATE TABLE IF NOT EXISTS evidence_source_revisions (
    team_id UUID NOT NULL,
    source_revision_id UUID NOT NULL DEFAULT gen_random_uuid(),
    source_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    revision_token TEXT NOT NULL,
    expected_previous_revision_token TEXT NOT NULL DEFAULT '',
    supersedes_revision_id UUID NULL,
    content_hash TEXT NOT NULL,
    envelope JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, source_revision_id),
    FOREIGN KEY (team_id, source_id) REFERENCES evidence_sources(team_id, source_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, supersedes_revision_id) REFERENCES evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_source_revisions_token_nonempty CHECK (btrim(revision_token) <> ''),
    CONSTRAINT evidence_source_revisions_hash_nonempty CHECK (btrim(content_hash) <> ''),
    CONSTRAINT evidence_source_revisions_envelope_object_check CHECK (jsonb_typeof(envelope) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS evidence_source_revisions_token_unique
    ON evidence_source_revisions(team_id, source_id, revision_token);

ALTER TABLE evidence_sources
    DROP CONSTRAINT IF EXISTS evidence_sources_current_revision_fk;

ALTER TABLE evidence_sources
    ADD CONSTRAINT evidence_sources_current_revision_fk
    FOREIGN KEY (team_id, current_revision_id)
    REFERENCES evidence_source_revisions(team_id, source_revision_id)
    ON DELETE RESTRICT
    DEFERRABLE INITIALLY IMMEDIATE;

CREATE TABLE IF NOT EXISTS evidence_fragments (
    team_id UUID NOT NULL,
    fragment_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    source_id UUID NULL,
    source_revision_id UUID NULL,
    evidence_index INTEGER NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    source_type TEXT NOT NULL DEFAULT 'conversation',
    authority TEXT NOT NULL DEFAULT 'primary',
    source_ref TEXT NOT NULL DEFAULT '',
    labels TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, fragment_id),
    UNIQUE (team_id, ingest_id, evidence_index),
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_id) REFERENCES evidence_sources(team_id, source_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_revision_id) REFERENCES evidence_source_revisions(team_id, source_revision_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_fragments_index_check CHECK (evidence_index >= 0),
    CONSTRAINT evidence_fragments_content_nonempty CHECK (btrim(content) <> ''),
    CONSTRAINT evidence_fragments_hash_nonempty CHECK (btrim(content_hash) <> ''),
    CONSTRAINT evidence_fragments_source_type_check CHECK (source_type IN ('conversation', 'document', 'observation', 'manual')),
    CONSTRAINT evidence_fragments_authority_check CHECK (authority IN ('primary', 'secondary', 'derived')),
    CONSTRAINT evidence_fragments_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS evidence_fragments_ingest_idx
    ON evidence_fragments(team_id, ingest_id, evidence_index ASC);

CREATE INDEX IF NOT EXISTS evidence_fragments_source_revision_idx
    ON evidence_fragments(team_id, source_revision_id, evidence_index ASC)
    WHERE source_revision_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS evidence_security_events (
    team_id UUID NOT NULL,
    security_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    fragment_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    event_kind TEXT NOT NULL,
    decision TEXT NOT NULL,
    scan_policy_hash TEXT NOT NULL DEFAULT '',
    actor_profile_id UUID NULL,
    reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, security_event_id),
    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, actor_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_security_event_kind_check
        CHECK (event_kind IN ('deterministic_scan', 'reviewer_signal', 'verifier_signal', 'quarantine_release')),
    CONSTRAINT evidence_security_decision_check CHECK (decision IN ('pass', 'guarded', 'quarantine', 'released')),
    CONSTRAINT evidence_security_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS evidence_security_events_fragment_idx
    ON evidence_security_events(team_id, fragment_id, created_at ASC, security_event_id ASC);

CREATE INDEX IF NOT EXISTS evidence_security_events_decision_idx
    ON evidence_security_events(team_id, decision, created_at DESC);

CREATE TABLE IF NOT EXISTS evidence_security_signals (
    team_id UUID NOT NULL,
    security_event_id UUID NOT NULL,
    signal_index INTEGER NOT NULL,
    owner_profile_id UUID NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT NOT NULL,
    span_start INTEGER NOT NULL,
    span_end INTEGER NOT NULL,
    quote TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, security_event_id, signal_index),
    FOREIGN KEY (team_id, security_event_id) REFERENCES evidence_security_events(team_id, security_event_id) ON DELETE CASCADE,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_security_signal_index_check CHECK (signal_index >= 0),
    CONSTRAINT evidence_security_signal_kind_check CHECK (kind IN (
        'role_control_spoofing',
        'instruction_override',
        'prompt_secret_extraction',
        'tool_exfiltration',
        'obfuscated_instruction',
        'hidden_control_markup'
    )),
    CONSTRAINT evidence_security_signal_severity_check CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    CONSTRAINT evidence_security_signal_span_check CHECK (span_start >= 0 AND span_end > span_start),
    CONSTRAINT evidence_security_signal_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS evidence_quarantines (
    team_id UUID NOT NULL,
    quarantine_id UUID NOT NULL DEFAULT gen_random_uuid(),
    fragment_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    reason TEXT NOT NULL DEFAULT '',
    released_by_profile_id UUID NULL,
    release_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, quarantine_id),
    UNIQUE (team_id, fragment_id),
    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, released_by_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_quarantines_status_check CHECK (status IN ('active', 'released')),
    CONSTRAINT evidence_quarantines_release_check CHECK (
        (status = 'active' AND released_at IS NULL)
        OR (status = 'released' AND released_at IS NOT NULL AND released_by_profile_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS evidence_quarantines_status_idx
    ON evidence_quarantines(team_id, status, created_at ASC);

CREATE TABLE IF NOT EXISTS placement_runs (
    team_id UUID NOT NULL,
    placement_run_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ NULL,
    worker_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, placement_run_id),
    UNIQUE (team_id, ingest_id),
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT placement_runs_status_check
        CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'failed')),
    CONSTRAINT placement_runs_attempts_check CHECK (attempts >= 0 AND max_attempts >= 1 AND attempts <= max_attempts),
    CONSTRAINT placement_runs_completion_check CHECK (
        (status IN ('completed', 'failed', 'quarantined') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed', 'failed', 'quarantined'))
    )
);

CREATE INDEX IF NOT EXISTS placement_runs_team_status_available_idx
    ON placement_runs(team_id, status, available_at ASC, created_at ASC, placement_run_id);

CREATE INDEX IF NOT EXISTS placement_runs_owner_created_idx
    ON placement_runs(team_id, owner_profile_id, created_at DESC);

CREATE TABLE IF NOT EXISTS placement_items (
    team_id UUID NOT NULL,
    placement_item_id UUID NOT NULL DEFAULT gen_random_uuid(),
    placement_run_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    evidence_index INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    category TEXT NOT NULL DEFAULT 'pending',
    result JSONB NOT NULL DEFAULT '{}'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, placement_item_id),
    UNIQUE (team_id, placement_run_id, evidence_index),
    FOREIGN KEY (team_id, placement_run_id) REFERENCES placement_runs(team_id, placement_run_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id) REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    CONSTRAINT placement_items_index_check CHECK (evidence_index >= 0),
    CONSTRAINT placement_items_status_check
        CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'quarantined')),
    CONSTRAINT placement_items_category_check
        CHECK (category IN ('pending', 'fragment_only', 'candidate', 'validated_claim', 'fact', 'quarantined', 'failed')),
    CONSTRAINT placement_items_result_object_check CHECK (jsonb_typeof(result) = 'object')
);

CREATE INDEX IF NOT EXISTS placement_items_run_idx
    ON placement_items(team_id, placement_run_id, evidence_index ASC);

CREATE TABLE IF NOT EXISTS placement_outcomes (
    team_id UUID NOT NULL,
    outcome_id UUID NOT NULL DEFAULT gen_random_uuid(),
    placement_run_id UUID NOT NULL,
    placement_item_id UUID NULL,
    owner_profile_id UUID NOT NULL,
    outcome_kind TEXT NOT NULL,
    status TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, outcome_id),
    FOREIGN KEY (team_id, placement_run_id) REFERENCES placement_runs(team_id, placement_run_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, placement_item_id) REFERENCES placement_items(team_id, placement_item_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id) REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT placement_outcomes_kind_nonempty CHECK (btrim(outcome_kind) <> ''),
    CONSTRAINT placement_outcomes_status_nonempty CHECK (btrim(status) <> ''),
    CONSTRAINT placement_outcomes_payload_object_check CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX IF NOT EXISTS placement_outcomes_run_idx
    ON placement_outcomes(team_id, placement_run_id, created_at ASC, outcome_id ASC);

DROP TRIGGER IF EXISTS evidence_source_revisions_append_only ON evidence_source_revisions;
CREATE TRIGGER evidence_source_revisions_append_only
    BEFORE UPDATE OR DELETE ON evidence_source_revisions
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS evidence_fragments_append_only ON evidence_fragments;
CREATE TRIGGER evidence_fragments_append_only
    BEFORE UPDATE OR DELETE ON evidence_fragments
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS evidence_security_events_append_only ON evidence_security_events;
CREATE TRIGGER evidence_security_events_append_only
    BEFORE UPDATE OR DELETE ON evidence_security_events
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS evidence_security_signals_append_only ON evidence_security_signals;
CREATE TRIGGER evidence_security_signals_append_only
    BEFORE UPDATE OR DELETE ON evidence_security_signals
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

DROP TRIGGER IF EXISTS placement_outcomes_append_only ON placement_outcomes;
CREATE TRIGGER placement_outcomes_append_only
    BEFORE UPDATE OR DELETE ON placement_outcomes
    FOR EACH ROW EXECUTE FUNCTION prevent_v2_append_only_mutation();

ALTER TABLE semantic_team_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_team_refs FORCE ROW LEVEL SECURITY;
ALTER TABLE semantic_profile_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_profile_refs FORCE ROW LEVEL SECURITY;
ALTER TABLE knowledge_ingests ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge_ingests FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_sources FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_source_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_source_revisions FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_fragments ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_fragments FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_security_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_security_events FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_security_signals ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_security_signals FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_quarantines ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_quarantines FORCE ROW LEVEL SECURITY;
ALTER TABLE placement_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE placement_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE placement_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE placement_items FORCE ROW LEVEL SECURITY;
ALTER TABLE placement_outcomes ENABLE ROW LEVEL SECURITY;
ALTER TABLE placement_outcomes FORCE ROW LEVEL SECURITY;

CREATE POLICY semantic_team_refs_select ON semantic_team_refs
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    );
CREATE POLICY semantic_team_refs_insert ON semantic_team_refs
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

CREATE POLICY semantic_profile_refs_select ON semantic_profile_refs
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    );
CREATE POLICY semantic_profile_refs_insert ON semantic_profile_refs
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'knowledge_ingests',
        'evidence_sources',
        'evidence_quarantines',
        'placement_runs',
        'placement_items'
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
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
            table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR UPDATE USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            ) WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )',
            table_name || '_update',
            table_name
        );
    END LOOP;

    FOREACH table_name IN ARRAY ARRAY[
        'evidence_source_revisions',
        'evidence_fragments',
        'evidence_security_events',
        'evidence_security_signals',
        'placement_outcomes'
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
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
                OR (
                    current_setting(''app.tx_mode'', true) = ''team''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )',
            table_name || '_insert',
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

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

DROP TABLE IF EXISTS placement_outcomes;
DROP TABLE IF EXISTS placement_items;
DROP TABLE IF EXISTS placement_runs;
DROP TABLE IF EXISTS evidence_quarantines;
DROP TABLE IF EXISTS evidence_security_signals;
DROP TABLE IF EXISTS evidence_security_events;
DROP TABLE IF EXISTS evidence_fragments;
ALTER TABLE IF EXISTS evidence_sources DROP CONSTRAINT IF EXISTS evidence_sources_current_revision_fk;
DROP TABLE IF EXISTS evidence_source_revisions;
DROP TABLE IF EXISTS evidence_sources;
DROP TABLE IF EXISTS knowledge_ingests;
DROP TABLE IF EXISTS semantic_profile_refs;
DROP TABLE IF EXISTS semantic_team_refs;
DROP FUNCTION IF EXISTS prevent_v2_append_only_mutation();
DROP INDEX IF EXISTS idx_team_profiles_team_id_id_unique;

-- +goose StatementEnd
