-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: creates empty intake tables/indexes; no existing table rewrite.
-- RLS impact: FORCE RLS restricts requests to their owned, allowed private generation.
-- Backfill: none; accepted event/request text is immutable from first intake.
-- Backward compatibility: deploy with the matching private-erasure manifest; stop old instances.
-- Rollback: disable SESSION_INGEST_ENABLED and retain schema/data; Down refuses history.

SELECT set_config('app.tx_mode', 'migration', true);

CREATE TABLE session_submissions (
    team_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    submission_id UUID NOT NULL,
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    intake JSONB NOT NULL CHECK (jsonb_typeof(intake) = 'object'),
    prior_context JSONB NOT NULL CHECK (jsonb_typeof(prior_context) = 'array'),
    new_event_indices JSONB NOT NULL CHECK (jsonb_typeof(new_event_indices) = 'array'),
    accepted_event_count INTEGER NOT NULL CHECK (accepted_event_count BETWEEN 0 AND 20),
    duplicate_event_count INTEGER NOT NULL CHECK (duplicate_event_count BETWEEN 0 AND 20),
    result JSONB NULL CHECK (result IS NULL OR jsonb_typeof(result) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, space_id, submission_id),
    UNIQUE (team_id, space_id, idempotency_key),
    FOREIGN KEY (team_id, space_id) REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CHECK (accepted_event_count + duplicate_event_count BETWEEN 1 AND 20)
);

CREATE TABLE session_events (
    team_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    identity_hash TEXT NOT NULL CHECK (identity_hash ~ '^sha256:[0-9a-f]{64}$'),
    submission_id UUID NOT NULL,
    event_index INTEGER NOT NULL CHECK (event_index BETWEEN 0 AND 19),
    framework TEXT NOT NULL CHECK (length(framework) BETWEEN 1 AND 256),
    app_name TEXT NOT NULL CHECK (length(app_name) BETWEEN 1 AND 256),
    user_id TEXT NOT NULL CHECK (length(user_id) BETWEEN 1 AND 256),
    session_id TEXT NOT NULL CHECK (length(session_id) BETWEEN 1 AND 256),
    event_id TEXT NOT NULL CHECK (length(event_id) BETWEEN 1 AND 256),
    body JSONB NOT NULL CHECK (jsonb_typeof(body) = 'object'),
    result JSONB NULL CHECK (result IS NULL OR jsonb_typeof(result) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, space_id, identity_hash),
    FOREIGN KEY (team_id, space_id, submission_id)
        REFERENCES session_submissions(team_id, space_id, submission_id) ON DELETE RESTRICT
);
CREATE INDEX session_events_context_idx ON session_events(team_id, space_id, created_at DESC, identity_hash);
CREATE TRIGGER session_events_private_content_at AFTER INSERT ON session_events
    FOR EACH ROW EXECUTE FUNCTION dense_mem_note_private_content();

CREATE TABLE session_extraction_checkpoints (
    team_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    submission_id UUID NOT NULL,
    window_index INTEGER NOT NULL CHECK (window_index BETWEEN -1 AND 7),
    body JSONB NOT NULL CHECK (jsonb_typeof(body) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, space_id, submission_id, window_index),
    FOREIGN KEY (team_id, space_id, submission_id)
        REFERENCES session_submissions(team_id, space_id, submission_id) ON DELETE RESTRICT
);

CREATE TABLE session_submission_receipts (
    team_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    receipt_id UUID NOT NULL DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL,
    body JSONB NOT NULL CHECK (jsonb_typeof(body) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, space_id, receipt_id),
    FOREIGN KEY (team_id, space_id, submission_id)
        REFERENCES session_submissions(team_id, space_id, submission_id) ON DELETE RESTRICT
);

CREATE TABLE session_submission_diagnostics (
    team_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    space_id UUID NOT NULL,
    space_generation BIGINT NOT NULL CHECK (space_generation > 0),
    diagnostic_id UUID NOT NULL DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL,
    body JSONB NOT NULL CHECK (jsonb_typeof(body) = 'object' AND octet_length(body::text) <= 67108864),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '7 days',
    PRIMARY KEY (team_id, space_id, diagnostic_id),
    FOREIGN KEY (team_id, space_id, submission_id)
        REFERENCES session_submissions(team_id, space_id, submission_id) ON DELETE RESTRICT,
    CHECK (expires_at >= created_at AND expires_at <= created_at + INTERVAL '7 days')
);
CREATE INDEX session_submission_diagnostics_expiry_idx ON session_submission_diagnostics(expires_at, diagnostic_id);

CREATE FUNCTION dense_mem_guard_session_diagnostic() RETURNS TRIGGER
LANGUAGE plpgsql AS $guard$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('app.tx_mode', true) = 'system' THEN
        IF NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = OLD.space_id THEN
            RETURN OLD;
        END IF;
        IF current_setting('app.session_diagnostic_purge', true) = 'true'
           AND OLD.expires_at <= clock_timestamp()
           AND NOT EXISTS (SELECT 1 FROM private_memory_legal_holds
               WHERE space_id = OLD.space_id AND released_at IS NULL) THEN
            RETURN OLD;
        END IF;
    END IF;
    RAISE EXCEPTION 'session diagnostics are immutable except for expired unheld retention or private erasure';
END $guard$;

CREATE FUNCTION dense_mem_guard_session_projection() RETURNS TRIGGER
LANGUAGE plpgsql AS $guard$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF current_setting('app.tx_mode', true) = 'system'
           AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = OLD.space_id THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'session intake can be deleted only by private erasure';
    END IF;
    IF (to_jsonb(NEW) - 'result') <> (to_jsonb(OLD) - 'result') THEN
        RAISE EXCEPTION 'session intake is immutable';
    END IF;
    IF OLD.result IS NOT NULL
       AND (TG_TABLE_NAME = 'session_events' OR OLD.result->>'processing_state' = 'completed') THEN
        RAISE EXCEPTION 'completed session result is immutable';
    END IF;
    RETURN NEW;
END $guard$;

DO $policies$
DECLARE table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'session_submissions', 'session_events', 'session_extraction_checkpoints', 'session_submission_receipts', 'session_submission_diagnostics'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format($policy$
            CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting('app.tx_mode', true) IN ('system', 'migration')
                OR (current_setting('app.tx_mode', true) = 'profile'
                    AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
                    AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid
                    AND dense_mem_space_allowed(space_id)
                    AND space_generation = dense_mem_active_space_generation(team_id, space_id))
            )
        $policy$, table_name || '_select', table_name);
        EXECUTE format($policy$
            CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                space_generation = dense_mem_active_space_generation(team_id, space_id)
                AND EXISTS (SELECT 1 FROM memory_spaces AS space
                    WHERE space.team_id = %I.team_id AND space.id = %I.space_id
                      AND space.kind IN ('profile_private', 'credential_private'))
                AND (current_setting('app.tx_mode', true) IN ('system', 'migration')
                    OR (current_setting('app.tx_mode', true) = 'profile'
                        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
                        AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid
                        AND dense_mem_space_allowed(space_id)))
            )
        $policy$, table_name || '_insert', table_name, table_name, table_name);
        EXECUTE format($policy$
            CREATE POLICY %I ON %I FOR DELETE USING (
                current_setting('app.tx_mode', true) = 'system'
                AND space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
            )
        $policy$, table_name || '_delete', table_name);
        IF table_name IN ('session_submissions', 'session_events') THEN
            EXECUTE format($policy$
                CREATE POLICY %I ON %I FOR UPDATE USING (
                    current_setting('app.tx_mode', true) = 'profile'
                    AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
                    AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid
                    AND dense_mem_space_allowed(space_id)
                    AND space_generation = dense_mem_active_space_generation(team_id, space_id)
                ) WITH CHECK (
                    current_setting('app.tx_mode', true) = 'profile'
                    AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
                    AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid
                    AND dense_mem_space_allowed(space_id)
                    AND space_generation = dense_mem_active_space_generation(team_id, space_id)
                )
            $policy$, table_name || '_update', table_name);
            EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_session_projection()', table_name || '_guard', table_name);
        ELSIF table_name = 'session_submission_diagnostics' THEN
            EXECUTE 'CREATE POLICY session_submission_diagnostics_retention ON session_submission_diagnostics FOR DELETE USING (current_setting(''app.tx_mode'', true) = ''system'' AND current_setting(''app.session_diagnostic_purge'', true) = ''true'' AND expires_at <= clock_timestamp() AND NOT EXISTS (SELECT 1 FROM private_memory_legal_holds WHERE space_id = session_submission_diagnostics.space_id AND released_at IS NULL))';
            EXECUTE 'CREATE TRIGGER session_submission_diagnostics_guard BEFORE UPDATE OR DELETE ON session_submission_diagnostics FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_session_diagnostic()';
        ELSE
            EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation()', table_name || '_guard', table_name);
        END IF;
    END LOOP;
END $policies$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
DO $rollback$
BEGIN
    IF EXISTS (SELECT 1 FROM session_submissions) THEN
        RAISE EXCEPTION 'cannot roll back populated session intake; disable intake and roll forward';
    END IF;
END $rollback$;
DROP TABLE session_submission_diagnostics;
DROP FUNCTION dense_mem_guard_session_diagnostic();
DROP TABLE session_submission_receipts;
DROP TABLE session_extraction_checkpoints;
DROP TABLE session_events;
DROP TABLE session_submissions;
DROP FUNCTION dense_mem_guard_session_projection();

-- +goose StatementEnd
