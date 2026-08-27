-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: this migration creates empty additive tables and
-- indexes, and adds nullable transition columns with no table rewrite. The
-- reconciliation counters use NOT VALID checks so a production-sized queue
-- is not scanned while the service is running.
-- RLS impact: attempts and events are visible to the owning team; raw failure
-- artifacts and assessment history are control-only. Inserts are limited to
-- the owner profile (or audited system and migration transactions).
-- Append-only rows cannot be updated or deleted by normal application code.
-- Backfill: none. Existing placement, assessment, and reconciliation history
-- remains byte-for-byte unchanged for the later stopped-service cutover.
-- Backward compatibility: the release binary does not read or write these
-- tables; legacy Remember intake and worker wiring remain unchanged.
-- Rollback: Down removes the empty foundation only. Once a foundation row is
-- written, history is an irreversible boundary and Down refuses to proceed.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('lock_timeout', '30s', true);

-- These nullable keys let the later stopped-service cutover retain the old
-- placement/assessment identifiers while it introduces terminal lineage.
ALTER TABLE relationship_observations
    ADD COLUMN IF NOT EXISTS remember_attempt_id UUID NULL;
ALTER TABLE entity_resolution_events
    ADD COLUMN IF NOT EXISTS remember_attempt_id UUID NULL,
    ADD COLUMN IF NOT EXISTS semantic_assessment_id UUID NULL;
ALTER TABLE verification_events
    ADD COLUMN IF NOT EXISTS remember_attempt_id UUID NULL,
    ADD COLUMN IF NOT EXISTS semantic_assessment_id UUID NULL;
ALTER TABLE review_tasks
    ADD COLUMN IF NOT EXISTS remember_attempt_id UUID NULL,
    ADD COLUMN IF NOT EXISTS semantic_assessment_id UUID NULL;
ALTER TABLE predicate_registration_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID NULL,
    ADD COLUMN IF NOT EXISTS remember_attempt_id UUID NULL,
    ADD COLUMN IF NOT EXISTS semantic_assessment_id UUID NULL;

-- Document-centric counters are additive. Legacy queue counters remain for
-- historical readers until the final reconciliation adoption ticket.
ALTER TABLE embedding_reconciliation_runs
    ADD COLUMN IF NOT EXISTS selected_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS embedded_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS updated_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS drifted_count BIGINT NOT NULL DEFAULT 0;
DO $dense_mem_document_counts_constraint$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'embedding_reconciliation_runs'::regclass
          AND conname = 'embedding_reconciliation_runs_document_counts_check'
    ) THEN
        ALTER TABLE embedding_reconciliation_runs
            ADD CONSTRAINT embedding_reconciliation_runs_document_counts_check
            CHECK (
                selected_count >= 0 AND embedded_count >= 0 AND
                updated_count >= 0 AND drifted_count >= 0
            ) NOT VALID;
    END IF;
END;
$dense_mem_document_counts_constraint$;

CREATE TABLE IF NOT EXISTS remember_attempts (
    team_id UUID NOT NULL,
    attempt_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    space_id UUID NULL,
    space_generation BIGINT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    contract_version TEXT NOT NULL,
    submission_kind TEXT NOT NULL DEFAULT 'remember',
    outcome TEXT NOT NULL,
    failed_phase TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL DEFAULT '',
    public_result JSONB NOT NULL DEFAULT '{}'::jsonb,
    canonical_attempt_id UUID NULL,
    evidence_count INTEGER NOT NULL DEFAULT 0,
    relationship_count INTEGER NOT NULL DEFAULT 0,
    document_count INTEGER NOT NULL DEFAULT 0,
    assessor_turns INTEGER NOT NULL DEFAULT 0,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    expires_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, attempt_id),
    UNIQUE (team_id, attempt_id, owner_profile_id),
    CONSTRAINT remember_attempts_outcome_check
        CHECK (outcome IN ('completed', 'rejected', 'quarantined', 'failed', 'replayed')),
    CONSTRAINT remember_attempts_result_check
        CHECK (jsonb_typeof(public_result) = 'object'),
    CONSTRAINT remember_attempts_counts_check CHECK (
        evidence_count >= 0 AND relationship_count >= 0 AND
        document_count >= 0 AND assessor_turns >= 0 AND duration_ms >= 0
    ),
    CONSTRAINT remember_attempts_space_pair_check CHECK (
        (space_id IS NULL AND space_generation IS NULL)
        OR (space_id IS NOT NULL AND space_generation > 0)
    ),
    CONSTRAINT remember_attempts_key_check
        CHECK (btrim(idempotency_key) <> '' AND btrim(request_hash) <> ''),
    CONSTRAINT remember_attempts_kind_check
        CHECK (submission_kind IN ('remember', 'relationship_correction')),
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS remember_attempts_canonical_key_idx
    ON remember_attempts(team_id, owner_profile_id, idempotency_key)
    WHERE outcome IN ('completed', 'rejected', 'quarantined');
CREATE INDEX IF NOT EXISTS remember_attempts_idempotency_key_idx
    ON remember_attempts(team_id, owner_profile_id, idempotency_key, created_at DESC, attempt_id DESC);
CREATE INDEX IF NOT EXISTS remember_attempts_owner_created_idx
    ON remember_attempts(team_id, owner_profile_id, created_at DESC, attempt_id DESC);
CREATE INDEX IF NOT EXISTS remember_attempts_expiry_idx
    ON remember_attempts(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS remember_attempt_events (
    team_id UUID NOT NULL,
    event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    attempt_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    sequence_no INTEGER NOT NULL,
    phase TEXT NOT NULL,
    event_kind TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, event_id),
    UNIQUE (team_id, attempt_id, sequence_no),
    CONSTRAINT remember_attempt_events_sequence_check CHECK (sequence_no >= 1),
    CONSTRAINT remember_attempt_events_phase_check CHECK (char_length(btrim(phase)) BETWEEN 1 AND 64),
    CONSTRAINT remember_attempt_events_kind_check CHECK (char_length(btrim(event_kind)) BETWEEN 1 AND 96),
    CONSTRAINT remember_attempt_events_outcome_check CHECK (char_length(outcome) <= 64),
    CONSTRAINT remember_attempt_events_metadata_check CHECK (
        jsonb_typeof(metadata) = 'object' AND pg_column_size(metadata) <= 16384
    ),
    FOREIGN KEY (team_id, attempt_id, owner_profile_id)
        REFERENCES remember_attempts(team_id, attempt_id, owner_profile_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS remember_attempt_events_attempt_idx
    ON remember_attempt_events(team_id, attempt_id, sequence_no);

CREATE TABLE IF NOT EXISTS remember_failure_artifacts (
    team_id UUID NOT NULL,
    artifact_id UUID NOT NULL DEFAULT gen_random_uuid(),
    attempt_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    artifact_kind TEXT NOT NULL,
    content_type TEXT NOT NULL,
    content_bytes BYTEA NOT NULL,
    byte_count BIGINT NOT NULL,
    content_sha256 TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (team_id, artifact_id),
    CONSTRAINT remember_failure_artifacts_kind_check
        CHECK (char_length(btrim(artifact_kind)) BETWEEN 1 AND 96),
    CONSTRAINT remember_failure_artifacts_content_type_check
        CHECK (char_length(btrim(content_type)) BETWEEN 1 AND 128),
    CONSTRAINT remember_failure_artifacts_size_check CHECK (
        byte_count = octet_length(content_bytes) AND byte_count BETWEEN 0 AND 262144
    ),
    CONSTRAINT remember_failure_artifacts_hash_check
        CHECK (content_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT remember_failure_artifacts_metadata_check CHECK (
        jsonb_typeof(metadata) = 'object' AND pg_column_size(metadata) <= 16384
    ),
    CONSTRAINT remember_failure_artifacts_expiry_check CHECK (expires_at >= captured_at),
    FOREIGN KEY (team_id, attempt_id, owner_profile_id)
        REFERENCES remember_attempts(team_id, attempt_id, owner_profile_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS remember_failure_artifacts_expiry_idx
    ON remember_failure_artifacts(expires_at);
CREATE INDEX IF NOT EXISTS remember_failure_artifacts_attempt_idx
    ON remember_failure_artifacts(team_id, attempt_id, captured_at);

CREATE TABLE IF NOT EXISTS semantic_assessments (
    team_id UUID NOT NULL,
    semantic_assessment_id UUID NOT NULL DEFAULT gen_random_uuid(),
    attempt_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    response_history JSONB NOT NULL DEFAULT '[]'::jsonb,
    accepted_revision INTEGER NULL,
    provider_turns INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    tokenizer TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    candidate_context_tokens INTEGER NOT NULL DEFAULT 0,
    candidate_context_truncated BOOLEAN NOT NULL DEFAULT false,
    response_hash TEXT NOT NULL DEFAULT '',
    validated_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, semantic_assessment_id),
    UNIQUE (team_id, attempt_id),
    CONSTRAINT semantic_assessments_history_check CHECK (
        jsonb_typeof(response_history) = 'array' AND pg_column_size(response_history) <= 1048576
    ),
    CONSTRAINT semantic_assessments_revision_check
        CHECK (accepted_revision IS NULL OR accepted_revision >= 1),
    CONSTRAINT semantic_assessments_token_counts_check CHECK (
        input_tokens >= 0 AND output_tokens >= 0 AND candidate_context_tokens >= 0
    ),
    CONSTRAINT semantic_assessments_turn_check CHECK (provider_turns BETWEEN 0 AND 5),
    CONSTRAINT semantic_assessments_hash_check CHECK (
        response_hash = '' OR response_hash ~ '^sha256:[0-9a-f]{64}$'
    ),
    FOREIGN KEY (team_id, attempt_id, owner_profile_id)
        REFERENCES remember_attempts(team_id, attempt_id, owner_profile_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS semantic_assessments_owner_created_idx
    ON semantic_assessments(team_id, owner_profile_id, created_at DESC, semantic_assessment_id DESC);

ALTER TABLE remember_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE remember_attempt_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_attempt_events FORCE ROW LEVEL SECURITY;
ALTER TABLE remember_failure_artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_failure_artifacts FORCE ROW LEVEL SECURITY;
ALTER TABLE semantic_assessments ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_assessments FORCE ROW LEVEL SECURITY;

DO $dense_mem_synchronous_write_policies$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'remember_attempts', 'remember_attempt_events',
        'remember_failure_artifacts', 'semantic_assessments'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_policies
            WHERE schemaname = 'public' AND tablename = table_name
              AND policyname = table_name || '_select'
        ) THEN
            IF table_name IN ('remember_failure_artifacts', 'semantic_assessments') THEN
                EXECUTE format($policy$
                    CREATE POLICY %I ON %I FOR SELECT USING (
                        current_setting('app.tx_mode', true) IN ('system', 'migration')
                    )
                $policy$, table_name || '_select', table_name);
            ELSE
                EXECUTE format($policy$
                    CREATE POLICY %I ON %I FOR SELECT USING (
                        current_setting('app.tx_mode', true) IN ('system', 'migration')
                        OR (
                            current_setting('app.tx_mode', true) IN ('team', 'profile')
                            AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
                        )
                    )
                $policy$, table_name || '_select', table_name);
            END IF;
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM pg_policies
            WHERE schemaname = 'public' AND tablename = table_name
              AND policyname = table_name || '_insert'
        ) THEN
            EXECUTE format($policy$
                CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                    current_setting('app.tx_mode', true) IN ('system', 'migration')
                    OR (
                        current_setting('app.tx_mode', true) = 'profile'
                        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
                        AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid
                    )
                )
            $policy$, table_name || '_insert', table_name);
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM pg_policies
            WHERE schemaname = 'public' AND tablename = table_name
              AND policyname = table_name || '_immutable_update'
        ) THEN
            EXECUTE format($policy$
                CREATE POLICY %I ON %I FOR UPDATE USING (
                    current_setting('app.tx_mode', true) IN ('system', 'migration')
                ) WITH CHECK (
                    current_setting('app.tx_mode', true) IN ('system', 'migration')
                )
            $policy$, table_name || '_immutable_update', table_name);
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM pg_policies
            WHERE schemaname = 'public' AND tablename = table_name
              AND policyname = table_name || '_immutable_delete'
        ) THEN
            EXECUTE format($policy$
                CREATE POLICY %I ON %I FOR DELETE USING (
                    current_setting('app.tx_mode', true) IN ('system', 'migration')
                )
            $policy$, table_name || '_immutable_delete', table_name);
        END IF;
    END LOOP;
END;
$dense_mem_synchronous_write_policies$;

CREATE OR REPLACE FUNCTION prevent_synchronous_write_append_only_mutation()
RETURNS TRIGGER AS $$
DECLARE
    tx_mode TEXT := current_setting('app.tx_mode', true);
BEGIN
    IF tx_mode IN ('system', 'migration') THEN
        IF TG_OP = 'UPDATE' THEN
            RETURN NEW;
        END IF;
        IF TG_TABLE_NAME = 'remember_failure_artifacts' THEN
            IF TG_OP = 'DELETE' THEN
                IF OLD.expires_at <= CURRENT_TIMESTAMP THEN
                    RETURN OLD;
                END IF;
                RAISE EXCEPTION 'remember_failure_artifacts cannot be purged before expires_at';
            END IF;
        END IF;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

DO $dense_mem_synchronous_write_triggers$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'remember_attempts'::regclass
          AND tgname = 'remember_attempts_append_only'
    ) THEN
        CREATE TRIGGER remember_attempts_append_only
            BEFORE UPDATE OR DELETE ON remember_attempts
            FOR EACH ROW EXECUTE FUNCTION prevent_synchronous_write_append_only_mutation();
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'remember_attempt_events'::regclass
          AND tgname = 'remember_attempt_events_append_only'
    ) THEN
        CREATE TRIGGER remember_attempt_events_append_only
            BEFORE UPDATE OR DELETE ON remember_attempt_events
            FOR EACH ROW EXECUTE FUNCTION prevent_synchronous_write_append_only_mutation();
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'remember_failure_artifacts'::regclass
          AND tgname = 'remember_failure_artifacts_append_only'
    ) THEN
        CREATE TRIGGER remember_failure_artifacts_append_only
            BEFORE UPDATE OR DELETE ON remember_failure_artifacts
            FOR EACH ROW EXECUTE FUNCTION prevent_synchronous_write_append_only_mutation();
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'semantic_assessments'::regclass
          AND tgname = 'semantic_assessments_append_only'
    ) THEN
        CREATE TRIGGER semantic_assessments_append_only
            BEFORE UPDATE OR DELETE ON semantic_assessments
            FOR EACH ROW EXECUTE FUNCTION prevent_synchronous_write_append_only_mutation();
    END IF;
END;
$dense_mem_synchronous_write_triggers$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_synchronous_write_foundation_down$
DECLARE
    foundation_rows BIGINT;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);
    PERFORM set_config('app.allowed_space_ids', '', true);
    SELECT
        (SELECT count(*) FROM remember_attempts) +
        (SELECT count(*) FROM remember_attempt_events) +
        (SELECT count(*) FROM remember_failure_artifacts) +
        (SELECT count(*) FROM semantic_assessments)
    INTO foundation_rows;
    IF foundation_rows > 0 THEN
        RAISE EXCEPTION
            '20260828010001 is irreversible after synchronous-write foundation history exists';
    END IF;

    DROP TRIGGER IF EXISTS remember_attempts_append_only ON remember_attempts;
    DROP TRIGGER IF EXISTS remember_attempt_events_append_only ON remember_attempt_events;
    DROP TRIGGER IF EXISTS remember_failure_artifacts_append_only ON remember_failure_artifacts;
    DROP TRIGGER IF EXISTS semantic_assessments_append_only ON semantic_assessments;
    DROP TABLE IF EXISTS semantic_assessments;
    DROP TABLE IF EXISTS remember_failure_artifacts;
    DROP TABLE IF EXISTS remember_attempt_events;
    DROP TABLE IF EXISTS remember_attempts;
    DROP FUNCTION IF EXISTS prevent_synchronous_write_append_only_mutation();

    ALTER TABLE embedding_reconciliation_runs
        DROP CONSTRAINT IF EXISTS embedding_reconciliation_runs_document_counts_check,
        DROP COLUMN IF EXISTS selected_count,
        DROP COLUMN IF EXISTS embedded_count,
        DROP COLUMN IF EXISTS updated_count,
        DROP COLUMN IF EXISTS drifted_count;

    ALTER TABLE relationship_observations DROP COLUMN IF EXISTS remember_attempt_id;
    ALTER TABLE entity_resolution_events
        DROP COLUMN IF EXISTS remember_attempt_id,
        DROP COLUMN IF EXISTS semantic_assessment_id;
    ALTER TABLE verification_events
        DROP COLUMN IF EXISTS remember_attempt_id,
        DROP COLUMN IF EXISTS semantic_assessment_id;
    ALTER TABLE review_tasks
        DROP COLUMN IF EXISTS remember_attempt_id,
        DROP COLUMN IF EXISTS semantic_assessment_id;
    ALTER TABLE predicate_registration_events
        DROP COLUMN IF EXISTS ingest_id,
        DROP COLUMN IF EXISTS remember_attempt_id,
        DROP COLUMN IF EXISTS semantic_assessment_id;
END;
$dense_mem_synchronous_write_foundation_down$;
-- +goose StatementEnd
