-- Lock/rewrite impact: additive tables and bounded indexes only; no existing
-- rows are rewritten. RLS impact: profile transactions may stage cited cases,
-- while system transactions may resolve or dismiss them. No backfill is
-- performed. Backward compatibility: the existing Remember and Recall
-- contracts remain unchanged; the new ledger is additive. Rollback refuses
-- once conflict history exists.

-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

CREATE TABLE IF NOT EXISTS evidence_conflict_cases (
    team_id UUID NOT NULL,
    conflict_id UUID NOT NULL DEFAULT gen_random_uuid(),
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    case_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    version INTEGER NOT NULL DEFAULT 1,
    preferred_position_id UUID NULL,
    resolved_at TIMESTAMPTZ NULL,
    resolution_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, conflict_id),
    UNIQUE (team_id, space_id, space_generation, case_key),
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CONSTRAINT evidence_conflict_cases_key_nonempty CHECK (btrim(case_key) <> ''),
    CONSTRAINT evidence_conflict_cases_status_check CHECK (status IN ('open', 'resolved', 'dismissed')),
    CONSTRAINT evidence_conflict_cases_version_check CHECK (version >= 1),
    CONSTRAINT evidence_conflict_cases_space_generation_check CHECK (space_generation > 0),
    CONSTRAINT evidence_conflict_cases_reason_length_check CHECK (length(resolution_reason) <= 512)
);

CREATE TABLE IF NOT EXISTS evidence_conflict_positions (
    team_id UUID NOT NULL,
    conflict_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    position_id UUID NOT NULL DEFAULT gen_random_uuid(),
    position_key TEXT NOT NULL,
    canonical_evidence_id UUID NOT NULL,
    canonical_owner_profile_id UUID NOT NULL,
    occurrence_id UUID NOT NULL,
    occurrence_owner_profile_id UUID NOT NULL,
    quote TEXT NOT NULL,
    span_start INTEGER NOT NULL,
    span_end INTEGER NOT NULL,
    authority TEXT NOT NULL,
    submitted BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, position_id),
    UNIQUE (team_id, conflict_id, position_key),
    FOREIGN KEY (team_id, conflict_id) REFERENCES evidence_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, canonical_evidence_id, canonical_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, occurrence_id, occurrence_owner_profile_id)
        REFERENCES evidence_occurrences(team_id, occurrence_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT evidence_conflict_positions_key_nonempty CHECK (btrim(position_key) <> ''),
    CONSTRAINT evidence_conflict_positions_quote_nonempty CHECK (quote <> ''),
    CONSTRAINT evidence_conflict_positions_span_check CHECK (span_start >= 0 AND span_end > span_start AND span_end - span_start <= 4000),
    CONSTRAINT evidence_conflict_positions_authority_check CHECK (authority IN ('authoritative', 'primary', 'secondary', 'inferred', 'unknown')),
    CONSTRAINT evidence_conflict_positions_space_generation_check CHECK (space_generation > 0)
);

CREATE TABLE IF NOT EXISTS evidence_conflict_events (
    team_id UUID NOT NULL,
    conflict_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    conflict_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL,
    ordinal BIGINT NOT NULL,
    action TEXT NOT NULL,
    status_after TEXT NOT NULL,
    case_version INTEGER NOT NULL,
    actor_kind TEXT NOT NULL,
    actor_id TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    preferred_position_id UUID NULL,
    citation_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, conflict_event_id),
    UNIQUE (team_id, conflict_id, ordinal),
    FOREIGN KEY (team_id, conflict_id) REFERENCES evidence_conflict_cases(team_id, conflict_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CONSTRAINT evidence_conflict_events_action_check CHECK (action IN ('opened', 'recited', 'resolved', 'dismissed')),
    CONSTRAINT evidence_conflict_events_status_check CHECK (status_after IN ('open', 'resolved', 'dismissed')),
    CONSTRAINT evidence_conflict_events_version_check CHECK (case_version >= 1),
    CONSTRAINT evidence_conflict_events_actor_kind_check CHECK (actor_kind IN ('profile', 'control', 'system')),
    CONSTRAINT evidence_conflict_events_reason_length_check CHECK (length(reason) <= 512),
    CONSTRAINT evidence_conflict_events_snapshot_check CHECK (jsonb_typeof(citation_snapshot) = 'array'),
    CONSTRAINT evidence_conflict_events_space_generation_check CHECK (space_generation > 0)
);

CREATE OR REPLACE FUNCTION validate_evidence_conflict_preferred_position()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.preferred_position_id IS NOT NULL
       AND NOT EXISTS (
           SELECT 1
           FROM evidence_conflict_positions AS position
           WHERE position.team_id = NEW.team_id
             AND position.conflict_id = NEW.conflict_id
             AND position.position_id = NEW.preferred_position_id
       )
    THEN
        RAISE EXCEPTION 'preferred position % does not belong to evidence conflict %', NEW.preferred_position_id, NEW.conflict_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS evidence_conflict_preferred_position_check ON evidence_conflict_cases;
CREATE TRIGGER evidence_conflict_preferred_position_check
    BEFORE INSERT OR UPDATE OF team_id, conflict_id, preferred_position_id ON evidence_conflict_cases
    FOR EACH ROW EXECUTE FUNCTION validate_evidence_conflict_preferred_position();

CREATE INDEX IF NOT EXISTS evidence_conflict_cases_activity_idx
    ON evidence_conflict_cases(team_id, status, updated_at DESC, conflict_id DESC);
CREATE INDEX IF NOT EXISTS evidence_conflict_cases_space_idx
    ON evidence_conflict_cases(team_id, space_id, space_generation, status, updated_at DESC, conflict_id DESC);
CREATE INDEX IF NOT EXISTS evidence_conflict_positions_evidence_idx
    ON evidence_conflict_positions(team_id, canonical_evidence_id, conflict_id);
CREATE INDEX IF NOT EXISTS evidence_conflict_events_history_idx
    ON evidence_conflict_events(team_id, conflict_id, ordinal DESC, conflict_event_id DESC);

-- Cases are inserted before their immutable positions, so the cardinality and
-- submitted-position invariant is checked at transaction end.
CREATE OR REPLACE FUNCTION validate_evidence_conflict_case_positions()
RETURNS TRIGGER AS $$
DECLARE
    position_count INTEGER;
    submitted_count INTEGER;
BEGIN
    SELECT count(*)::integer, count(*) FILTER (WHERE submitted)::integer
      INTO position_count, submitted_count
      FROM evidence_conflict_positions
     WHERE team_id = NEW.team_id AND conflict_id = NEW.conflict_id;
    IF position_count < 2 OR position_count > 10 OR submitted_count < 1 THEN
        RAISE EXCEPTION 'evidence conflict case % must have 2-10 positions and a submitted position', NEW.conflict_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS evidence_conflict_cases_positions_check ON evidence_conflict_cases;
CREATE CONSTRAINT TRIGGER evidence_conflict_cases_positions_check
    AFTER INSERT OR UPDATE ON evidence_conflict_cases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION validate_evidence_conflict_case_positions();

DROP TRIGGER IF EXISTS evidence_conflict_positions_case_check ON evidence_conflict_positions;
CREATE CONSTRAINT TRIGGER evidence_conflict_positions_case_check
    AFTER INSERT ON evidence_conflict_positions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION validate_evidence_conflict_case_positions();

CREATE OR REPLACE FUNCTION prevent_evidence_conflict_case_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = OLD.space_id
    THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND (
           (
               current_setting('app.tx_mode', true) = 'profile'
               AND OLD.status = 'open'
               AND NEW.status = 'open'
               AND NEW.version = OLD.version + 1
               AND NEW.team_id = OLD.team_id
               AND NEW.space_id = OLD.space_id
               AND NEW.space_generation = OLD.space_generation
               AND NEW.case_key = OLD.case_key
               AND NEW.preferred_position_id IS NOT DISTINCT FROM OLD.preferred_position_id
               AND NEW.resolved_at IS NOT DISTINCT FROM OLD.resolved_at
               AND NEW.resolution_reason = OLD.resolution_reason
               AND NEW.created_at = OLD.created_at
           )
           OR (
               current_setting('app.tx_mode', true) = 'system'
               AND OLD.status = 'open'
               AND NEW.status IN ('resolved', 'dismissed')
               AND NEW.version = OLD.version + 1
               AND NEW.team_id = OLD.team_id
               AND NEW.space_id = OLD.space_id
               AND NEW.space_generation = OLD.space_generation
               AND NEW.case_key = OLD.case_key
               AND NEW.created_at = OLD.created_at
           )
           -- A terminal recitation refreshes activity without reopening or versioning the case.
           OR (
               current_setting('app.tx_mode', true) = 'profile'
               AND OLD.status IN ('resolved', 'dismissed')
               AND NEW.status = OLD.status
               AND NEW.version = OLD.version
               AND NEW.team_id = OLD.team_id
               AND NEW.space_id = OLD.space_id
               AND NEW.space_generation = OLD.space_generation
               AND NEW.case_key = OLD.case_key
               AND NEW.preferred_position_id IS NOT DISTINCT FROM OLD.preferred_position_id
               AND NEW.resolved_at IS NOT DISTINCT FROM OLD.resolved_at
               AND NEW.resolution_reason = OLD.resolution_reason
               AND NEW.created_at = OLD.created_at
               AND NEW.updated_at > OLD.updated_at
           )
       )
    THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION '% is append-only except for the bounded state transition', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS evidence_conflict_cases_state_guard ON evidence_conflict_cases;
CREATE TRIGGER evidence_conflict_cases_state_guard
    BEFORE UPDATE OR DELETE ON evidence_conflict_cases
    FOR EACH ROW EXECUTE FUNCTION prevent_evidence_conflict_case_mutation();

DROP TRIGGER IF EXISTS evidence_conflict_positions_append_only ON evidence_conflict_positions;
CREATE TRIGGER evidence_conflict_positions_append_only
    BEFORE UPDATE OR DELETE ON evidence_conflict_positions
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();
DROP TRIGGER IF EXISTS evidence_conflict_events_append_only ON evidence_conflict_events;
CREATE TRIGGER evidence_conflict_events_append_only
    BEFORE UPDATE OR DELETE ON evidence_conflict_events
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE evidence_conflict_cases ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_conflict_cases FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_conflict_positions ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_conflict_positions FORCE ROW LEVEL SECURITY;
ALTER TABLE evidence_conflict_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_conflict_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS evidence_conflict_cases_select ON evidence_conflict_cases;
CREATE POLICY evidence_conflict_cases_select ON evidence_conflict_cases FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        AND (space_id = dense_mem_team_shared_space(team_id) OR dense_mem_space_allowed(space_id))
    )
);
DROP POLICY IF EXISTS evidence_conflict_cases_profile_insert ON evidence_conflict_cases;
CREATE POLICY evidence_conflict_cases_profile_insert ON evidence_conflict_cases FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        AND (space_id = dense_mem_team_shared_space(team_id) OR dense_mem_space_allowed(space_id))
        AND status = 'open'
    )
);
DROP POLICY IF EXISTS evidence_conflict_cases_update ON evidence_conflict_cases;
CREATE POLICY evidence_conflict_cases_update ON evidence_conflict_cases FOR UPDATE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        AND (space_id = dense_mem_team_shared_space(team_id) OR dense_mem_space_allowed(space_id))
    )
) WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        AND (space_id = dense_mem_team_shared_space(team_id) OR dense_mem_space_allowed(space_id))
        AND status IN ('open', 'resolved', 'dismissed')
    )
);
DROP POLICY IF EXISTS evidence_conflict_cases_erasure_delete ON evidence_conflict_cases;
CREATE POLICY evidence_conflict_cases_erasure_delete ON evidence_conflict_cases FOR DELETE USING (
    current_setting('app.tx_mode', true) = 'system'
    AND space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
);

DO $policies$
DECLARE table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['evidence_conflict_positions', 'evidence_conflict_events'] LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_select', table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (
            current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
            OR (current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                AND (space_id = dense_mem_team_shared_space(team_id) OR dense_mem_space_allowed(space_id)))
        )', table_name || '_select', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_insert', table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
            current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
            OR (current_setting(''app.tx_mode'', true) = ''profile''
                AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                AND (space_id = dense_mem_team_shared_space(team_id) OR dense_mem_space_allowed(space_id)))
        )', table_name || '_insert', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_erasure_delete', table_name);
        EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (
            current_setting(''app.tx_mode'', true) = ''system''
            AND space_id = NULLIF(current_setting(''app.private_erasure_space_id'', true), '''')::uuid
        )', table_name || '_erasure_delete', table_name);
    END LOOP;
END $policies$;

UPDATE app_config
SET value = regexp_replace(to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.?0+Z$', 'Z'),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM evidence_conflict_events)
       OR EXISTS (SELECT 1 FROM evidence_conflict_positions)
       OR EXISTS (SELECT 1 FROM evidence_conflict_cases)
    THEN
        RAISE EXCEPTION 'cannot roll back 20260904010001: evidence conflict history exists';
    END IF;
END $$;

DROP POLICY IF EXISTS evidence_conflict_events_erasure_delete ON evidence_conflict_events;
DROP POLICY IF EXISTS evidence_conflict_events_insert ON evidence_conflict_events;
DROP POLICY IF EXISTS evidence_conflict_events_select ON evidence_conflict_events;
DROP POLICY IF EXISTS evidence_conflict_positions_erasure_delete ON evidence_conflict_positions;
DROP POLICY IF EXISTS evidence_conflict_positions_insert ON evidence_conflict_positions;
DROP POLICY IF EXISTS evidence_conflict_positions_select ON evidence_conflict_positions;
DROP POLICY IF EXISTS evidence_conflict_cases_erasure_delete ON evidence_conflict_cases;
DROP POLICY IF EXISTS evidence_conflict_cases_update ON evidence_conflict_cases;
DROP POLICY IF EXISTS evidence_conflict_cases_profile_insert ON evidence_conflict_cases;
DROP POLICY IF EXISTS evidence_conflict_cases_select ON evidence_conflict_cases;

DROP TRIGGER IF EXISTS evidence_conflict_events_append_only ON evidence_conflict_events;
DROP TRIGGER IF EXISTS evidence_conflict_positions_append_only ON evidence_conflict_positions;
DROP TRIGGER IF EXISTS evidence_conflict_cases_state_guard ON evidence_conflict_cases;
DROP FUNCTION IF EXISTS prevent_evidence_conflict_case_mutation();
DROP TRIGGER IF EXISTS evidence_conflict_positions_case_check ON evidence_conflict_positions;
DROP TRIGGER IF EXISTS evidence_conflict_cases_positions_check ON evidence_conflict_cases;
DROP FUNCTION IF EXISTS validate_evidence_conflict_case_positions();
DROP TRIGGER IF EXISTS evidence_conflict_preferred_position_check ON evidence_conflict_cases;
DROP FUNCTION IF EXISTS validate_evidence_conflict_preferred_position();
DROP TABLE IF EXISTS evidence_conflict_events;
DROP TABLE IF EXISTS evidence_conflict_positions;
DROP TABLE IF EXISTS evidence_conflict_cases;

UPDATE app_config
SET value = regexp_replace(to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '\.?0+Z$', 'Z'),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
