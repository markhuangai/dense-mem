-- +goose Up
-- +goose StatementBegin

-- v2.6.1 synchronous Remember terminal history and the stopped-service hard
-- cutover. The migration drains and verifies the old workflow, then removes
-- its tables in the same release; there is no compatibility runtime.
-- Lock/rewrite impact: history copy and retirement abort on lock timeout.
-- RLS impact: attempts, events, artifacts, and assessments are owner-scoped;
-- system and migration modes are the only administrative access paths.
-- Backfill: Remember-origin history is copied into attempts/events/assessments
-- before the retired tables are dropped, with count and hash assertions.
-- Backward compatibility: none at runtime; this stopped-service migration is
-- the immediate v2.6.1 hard cutover and does not preserve a polling or worker
-- compatibility path.
-- The migration writes the v2.6.1 compatible authority marker only after all
-- copy, lineage, vector, and retired-table assertions succeed.
-- Rollback: the Down section is intentionally irreversible after these
-- append-only histories exist; restore a verified snapshot and boot the
-- previous binary.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('lock_timeout', '30s', true);

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
    CONSTRAINT remember_attempts_outcome_check CHECK (outcome IN ('completed', 'rejected', 'quarantined', 'failed', 'replayed')),
    CONSTRAINT remember_attempts_result_check CHECK (jsonb_typeof(public_result) = 'object'),
    CONSTRAINT remember_attempts_counts_check CHECK (evidence_count >= 0 AND relationship_count >= 0 AND document_count >= 0 AND assessor_turns >= 0),
    CONSTRAINT remember_attempts_space_pair_check CHECK ((space_id IS NULL AND space_generation IS NULL) OR (space_id IS NOT NULL AND space_generation > 0)),
    CONSTRAINT remember_attempts_key_check CHECK (btrim(idempotency_key) <> '' AND btrim(request_hash) <> ''),
    CONSTRAINT remember_attempts_kind_check CHECK (submission_kind IN ('remember', 'relationship_correction')),
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
    CONSTRAINT remember_attempt_events_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
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
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (team_id, artifact_id),
    CONSTRAINT remember_failure_artifacts_size_check CHECK (byte_count = octet_length(content_bytes) AND byte_count >= 0),
    CONSTRAINT remember_failure_artifacts_hash_check CHECK (btrim(content_sha256) <> ''),
    CONSTRAINT remember_failure_artifacts_expiry_check CHECK (expires_at >= captured_at),
    FOREIGN KEY (team_id, attempt_id, owner_profile_id)
        REFERENCES remember_attempts(team_id, attempt_id, owner_profile_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS remember_failure_artifacts_expiry_idx
    ON remember_failure_artifacts(expires_at);
CREATE INDEX IF NOT EXISTS remember_failure_artifacts_attempt_idx
    ON remember_failure_artifacts(team_id, attempt_id);

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
    CONSTRAINT semantic_assessments_history_check CHECK (jsonb_typeof(response_history) = 'array'),
    CONSTRAINT semantic_assessments_revision_check CHECK (accepted_revision IS NULL OR accepted_revision >= 1),
    CONSTRAINT semantic_assessments_token_counts_check CHECK (input_tokens >= 0 AND output_tokens >= 0 AND candidate_context_tokens >= 0),
    -- Historical placement assessments may contain up to five provider turns;
    -- new application responses remain bounded to three turns by validation.
    CONSTRAINT semantic_assessments_turn_check CHECK (provider_turns BETWEEN 0 AND 5),
    FOREIGN KEY (team_id, attempt_id, owner_profile_id)
        REFERENCES remember_attempts(team_id, attempt_id, owner_profile_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (team_id, attempt_id, owner_profile_id)
        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT
);

ALTER TABLE remember_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE remember_attempt_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_attempt_events FORCE ROW LEVEL SECURITY;
ALTER TABLE remember_failure_artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_failure_artifacts FORCE ROW LEVEL SECURITY;
ALTER TABLE semantic_assessments ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_assessments FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS remember_attempts_select ON remember_attempts;
DROP POLICY IF EXISTS remember_attempts_insert ON remember_attempts;
DROP POLICY IF EXISTS remember_attempts_delete ON remember_attempts;
CREATE POLICY remember_attempts_select ON remember_attempts
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY remember_attempts_insert ON remember_attempts
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY remember_attempts_delete ON remember_attempts
    FOR DELETE USING (
        current_setting('app.tx_mode', true) = 'system'
        AND space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
    );

DROP POLICY IF EXISTS remember_attempt_events_select ON remember_attempt_events;
DROP POLICY IF EXISTS remember_attempt_events_insert ON remember_attempt_events;
DROP POLICY IF EXISTS remember_attempt_events_delete ON remember_attempt_events;
CREATE POLICY remember_attempt_events_select ON remember_attempt_events
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY remember_attempt_events_insert ON remember_attempt_events
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY remember_attempt_events_delete ON remember_attempt_events
    FOR DELETE USING (
        current_setting('app.tx_mode', true) = 'system'
        AND EXISTS (
            SELECT 1
            FROM remember_attempts AS attempt
            WHERE attempt.team_id = remember_attempt_events.team_id
              AND attempt.attempt_id = remember_attempt_events.attempt_id
              AND attempt.owner_profile_id = remember_attempt_events.owner_profile_id
              AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS remember_failure_artifacts_select ON remember_failure_artifacts;
DROP POLICY IF EXISTS remember_failure_artifacts_insert ON remember_failure_artifacts;
DROP POLICY IF EXISTS remember_failure_artifacts_delete ON remember_failure_artifacts;
CREATE POLICY remember_failure_artifacts_select ON remember_failure_artifacts
    FOR SELECT USING (current_setting('app.tx_mode', true) IN ('system', 'migration'));
CREATE POLICY remember_failure_artifacts_insert ON remember_failure_artifacts
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
-- The purge worker locks candidate rows before deleting them. PostgreSQL
-- applies the UPDATE policy to SELECT ... FOR UPDATE, while the append-only
-- trigger below still rejects every update operation.
DROP POLICY IF EXISTS remember_failure_artifacts_lock ON remember_failure_artifacts;
CREATE POLICY remember_failure_artifacts_lock ON remember_failure_artifacts
    FOR UPDATE USING (current_setting('app.tx_mode', true) IN ('system', 'migration'))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration'));
CREATE POLICY remember_failure_artifacts_delete ON remember_failure_artifacts
    FOR DELETE USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        AND EXISTS (
            SELECT 1
            FROM remember_attempts AS attempt
            WHERE attempt.team_id = remember_failure_artifacts.team_id
              AND attempt.attempt_id = remember_failure_artifacts.attempt_id
              AND attempt.owner_profile_id = remember_failure_artifacts.owner_profile_id
              AND (
                  current_setting('app.remember_failure_artifact_purge', true) = 'true'
                  OR (
                  current_setting('app.tx_mode', true) = 'migration'
                  OR attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
                  )
              )
        )
    );

DROP POLICY IF EXISTS semantic_assessments_select ON semantic_assessments;
DROP POLICY IF EXISTS semantic_assessments_insert ON semantic_assessments;
DROP POLICY IF EXISTS semantic_assessments_delete ON semantic_assessments;
CREATE POLICY semantic_assessments_select ON semantic_assessments
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY semantic_assessments_insert ON semantic_assessments
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY semantic_assessments_delete ON semantic_assessments
    FOR DELETE USING (
        current_setting('app.tx_mode', true) = 'system'
        AND EXISTS (
            SELECT 1
            FROM remember_attempts AS attempt
            WHERE attempt.team_id = semantic_assessments.team_id
              AND attempt.attempt_id = semantic_assessments.attempt_id
              AND attempt.owner_profile_id = semantic_assessments.owner_profile_id
              AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
        )
    );

-- Event and artifact rows inherit their erasure space from the immutable
-- attempt because they intentionally do not duplicate space_id.
CREATE OR REPLACE FUNCTION prevent_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND (
           NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               = NULLIF(to_jsonb(OLD)->>'space_id', '')::uuid
           OR (
               TG_TABLE_NAME IN ('remember_attempt_events', 'remember_failure_artifacts', 'semantic_assessments')
               AND EXISTS (
                   SELECT 1
                   FROM remember_attempts AS attempt
                   WHERE attempt.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND attempt.attempt_id = NULLIF(to_jsonb(OLD)->>'attempt_id', '')::uuid
                     AND attempt.owner_profile_id = NULLIF(to_jsonb(OLD)->>'owner_profile_id', '')::uuid
                     AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               )
           )
           OR (
               TG_TABLE_NAME = 'remember_failure_artifacts'
               AND current_setting('app.remember_failure_artifact_purge', true) = 'true'
           )
           OR (
               TG_TABLE_NAME = 'relationship_cross_references'
               AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = (
                   SELECT target.space_id
                   FROM relationship_records AS target
                   WHERE target.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND target.relationship_id = NULLIF(to_jsonb(OLD)->>'target_relationship_id', '')::uuid
               )
           )
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS semantic_assessments_append_only ON semantic_assessments;
CREATE TRIGGER semantic_assessments_append_only
    BEFORE UPDATE OR DELETE ON semantic_assessments
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

DROP TRIGGER IF EXISTS remember_attempts_append_only ON remember_attempts;
CREATE TRIGGER remember_attempts_append_only
    BEFORE UPDATE OR DELETE ON remember_attempts
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();
DROP TRIGGER IF EXISTS remember_attempt_events_append_only ON remember_attempt_events;
CREATE TRIGGER remember_attempt_events_append_only
    BEFORE UPDATE OR DELETE ON remember_attempt_events
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- Copy Remember-origin history into the terminal-attempt and chronological
-- event projections before the legacy placement tables are retired. The
-- source rows remain untouched until the final stopped-service release step.
WITH legacy AS MATERIALIZED (
    SELECT ingest.team_id, ingest.ingest_id, ingest.owner_profile_id,
           ingest.space_id, ingest.space_generation,
           ingest.idempotency_key, ingest.request_hash, ingest.created_at,
           ingest.completed_at, ingest.status,
           COALESCE(run.status, ingest.status) AS run_status,
           COALESCE(run.attempts, 0) AS attempts,
           COALESCE(run.max_attempts, 0) AS max_attempts,
           COALESCE(ingest.metadata #>> '{actor,correlation_id}', '') AS correlation_id,
           (SELECT count(*) FROM evidence_fragments AS fragment
            WHERE fragment.team_id = ingest.team_id AND fragment.ingest_id = ingest.ingest_id) AS evidence_count,
           (SELECT count(*) FROM relationship_observations AS observation
            WHERE observation.team_id = ingest.team_id AND observation.ingest_id = ingest.ingest_id) AS relationship_count
    FROM knowledge_ingests AS ingest
    LEFT JOIN placement_runs AS run
      ON run.team_id = ingest.team_id AND run.ingest_id = ingest.ingest_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
), normalized AS (
    SELECT legacy.*,
           CASE
             WHEN legacy.run_status IN ('completed') OR legacy.status = 'completed' THEN 'completed'
             WHEN legacy.run_status IN ('rejected') OR legacy.status = 'rejected' THEN 'rejected'
             WHEN legacy.run_status IN ('quarantined') OR legacy.status = 'quarantined' THEN 'quarantined'
             ELSE 'failed'
           END AS outcome
    FROM legacy
)
INSERT INTO remember_attempts (
    team_id, attempt_id, owner_profile_id, space_id, space_generation,
    idempotency_key, request_hash, contract_version, submission_kind, outcome,
    error_code, correlation_id, public_result, canonical_attempt_id,
    evidence_count, relationship_count, created_at, completed_at
)
SELECT normalized.team_id, normalized.ingest_id, normalized.owner_profile_id,
       normalized.space_id, normalized.space_generation,
       COALESCE(NULLIF(normalized.idempotency_key, ''), 'legacy:' || normalized.ingest_id::text),
       COALESCE(NULLIF(normalized.request_hash, ''), 'legacy:' || normalized.ingest_id::text),
       'dense-mem.v2.6', 'remember', normalized.outcome,
       CASE WHEN normalized.outcome = 'failed' THEN 'internal_failure' ELSE '' END,
       normalized.correlation_id,
       jsonb_build_object(
           'contract_version', 'dense-mem.v2.6.1',
           'submission_id', normalized.ingest_id::text,
           'submission_kind', 'remember',
           'processing_state', normalized.outcome,
           'search_state', CASE WHEN normalized.outcome = 'completed' THEN 'current' ELSE 'not_required' END,
           'correlation_id', normalized.correlation_id,
           'evidence', '[]'::jsonb,
           'relationship_results', '[]'::jsonb,
           'errors', CASE WHEN normalized.outcome = 'failed' THEN jsonb_build_array(jsonb_build_object(
               'code', 'internal_failure', 'message', 'legacy Remember result was migrated during the v2.6.1 cutover',
               'retryable', true, 'next_action', 'retry_same_request', 'remediation', 'Retry the complete request with the same idempotency key.'
           )) ELSE '[]'::jsonb END
       ),
       normalized.ingest_id, normalized.evidence_count, normalized.relationship_count,
       normalized.created_at, COALESCE(normalized.completed_at, now())
FROM normalized
ON CONFLICT DO NOTHING;

WITH legacy_events AS (
    SELECT attempt.team_id, attempt.attempt_id, attempt.owner_profile_id,
           COALESCE(attempt.completed_at, attempt.created_at) AS occurred_at,
           0 AS event_rank, 'legacy_terminalized' AS event_kind,
           attempt.outcome,
           jsonb_build_object(
               'source', 'placement_runs',
               'contract_version', 'dense-mem.v2.6.1',
               'legacy_run_id', attempt.attempt_id
           ) AS metadata
    FROM remember_attempts AS attempt
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = attempt.team_id
     AND ingest.ingest_id = attempt.attempt_id
     AND ingest.owner_profile_id = attempt.owner_profile_id
    WHERE attempt.submission_kind = 'remember'
      AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'

    UNION ALL

    SELECT attempt.team_id, attempt.attempt_id, attempt.owner_profile_id,
           item.created_at, 1, 'legacy_item', item.status,
	           jsonb_build_object(
	               'source', 'placement_items',
	               'legacy_item_id', item.placement_item_id,
	               'evidence_index', item.evidence_index,
	               'status', item.status,
	               'category', item.category,
	               'error_present', btrim(item.error) <> '',
	               'error_sha256', CASE WHEN btrim(item.error) = '' THEN '' ELSE 'sha256:' || encode(digest(item.error, 'sha256'), 'hex') END,
	               'result_sha256', 'sha256:' || encode(digest(COALESCE(item.result::text, '{}'), 'sha256'), 'hex')
	           )
    FROM remember_attempts AS attempt
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = attempt.team_id
     AND ingest.ingest_id = attempt.attempt_id
     AND ingest.owner_profile_id = attempt.owner_profile_id
    JOIN placement_items AS item
      ON item.team_id = attempt.team_id
     AND item.ingest_id = attempt.attempt_id
     AND item.owner_profile_id = attempt.owner_profile_id
    WHERE attempt.submission_kind = 'remember'
      AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'

    UNION ALL

    SELECT attempt.team_id, attempt.attempt_id, attempt.owner_profile_id,
           outcome.created_at, 2, 'legacy_outcome', outcome.status,
	           jsonb_build_object(
	               'source', 'placement_outcomes',
	               'legacy_outcome_id', outcome.outcome_id,
	               'outcome_kind', outcome.outcome_kind,
	               'status', outcome.status,
	               'payload_sha256', 'sha256:' || encode(digest(COALESCE(outcome.payload::text, '{}'), 'sha256'), 'hex')
	           )
    FROM remember_attempts AS attempt
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = attempt.team_id
     AND ingest.ingest_id = attempt.attempt_id
     AND ingest.owner_profile_id = attempt.owner_profile_id
    JOIN placement_runs AS run
      ON run.team_id = attempt.team_id
     AND run.ingest_id = attempt.attempt_id
     AND run.owner_profile_id = attempt.owner_profile_id
    JOIN placement_outcomes AS outcome
      ON outcome.team_id = run.team_id
     AND outcome.placement_run_id = run.placement_run_id
     AND outcome.owner_profile_id = run.owner_profile_id
    WHERE attempt.submission_kind = 'remember'
      AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
), numbered AS (
    SELECT legacy_events.*,
           row_number() OVER (
               PARTITION BY team_id, attempt_id
               ORDER BY occurred_at, event_rank, event_kind, metadata::text
           )::INTEGER AS sequence_no
    FROM legacy_events
)
INSERT INTO remember_attempt_events (
    team_id, event_id, attempt_id, owner_profile_id, sequence_no,
    phase, event_kind, outcome, metadata, created_at
)
SELECT numbered.team_id, gen_random_uuid(), numbered.attempt_id,
       numbered.owner_profile_id, numbered.sequence_no,
       'legacy_cutover', numbered.event_kind, numbered.outcome,
       numbered.metadata, numbered.occurred_at
FROM numbered
WHERE NOT EXISTS (
    SELECT 1
    FROM remember_attempt_events AS event
    WHERE event.team_id = numbered.team_id
      AND event.attempt_id = numbered.attempt_id
      AND event.sequence_no = numbered.sequence_no
);

-- Preserve immutable assessor response history under the ingest key. Include
-- both the completed placement assessment and every validated response
-- revision; the migration never invents provider bytes.
WITH assessment_rows AS (
    SELECT legacy.team_id, legacy.ingest_id, legacy.owner_profile_id,
           placement.assessment_id, 0 AS revision_number,
           placement.normalized_response, placement.response_hash,
           placement.provider_turns, placement.model, placement.tokenizer,
           placement.input_tokens, placement.output_tokens,
           placement.candidate_context_tokens, placement.candidate_context_truncated,
           placement.validated_at
    FROM placement_assessments AS placement
    JOIN knowledge_ingests AS legacy
      ON legacy.team_id = placement.team_id
     AND legacy.ingest_id = placement.ingest_id
     AND legacy.owner_profile_id = placement.owner_profile_id
    WHERE placement.assessment_scope = 'submission'
    UNION ALL
    SELECT legacy.team_id, legacy.ingest_id, legacy.owner_profile_id,
           placement.assessment_id, 0 AS revision_number,
           placement.normalized_response, placement.response_hash,
           placement.provider_turns, placement.model, placement.tokenizer,
           placement.input_tokens, placement.output_tokens,
           placement.candidate_context_tokens, placement.candidate_context_truncated,
           placement.validated_at
    FROM placement_assessments AS placement
    JOIN placement_items AS item
      ON item.team_id = placement.team_id
     AND item.placement_item_id = placement.placement_item_id
     AND item.owner_profile_id = placement.owner_profile_id
    JOIN knowledge_ingests AS legacy
      ON legacy.team_id = item.team_id
     AND legacy.ingest_id = item.ingest_id
     AND legacy.owner_profile_id = item.owner_profile_id
    WHERE placement.assessment_scope = 'item'
    UNION ALL
    SELECT revision.team_id, revision.ingest_id, revision.owner_profile_id,
           revision.assessment_id, revision.revision_number,
           revision.normalized_response, revision.response_hash,
           revision.provider_turns, '' AS model, '' AS tokenizer,
           revision.input_tokens, revision.output_tokens,
           revision.candidate_context_tokens, revision.candidate_context_truncated,
           revision.validated_at
    FROM submission_assessment_response_revisions AS revision
), deduplicated AS (
    SELECT DISTINCT ON (team_id, ingest_id, owner_profile_id, assessment_id, revision_number)
           team_id, ingest_id, owner_profile_id, assessment_id, revision_number,
           normalized_response, response_hash, provider_turns, model, tokenizer,
           input_tokens, output_tokens, candidate_context_tokens,
           candidate_context_truncated, validated_at
    FROM assessment_rows
    ORDER BY team_id, ingest_id, owner_profile_id, assessment_id, revision_number,
             validated_at DESC
), grouped AS (
    SELECT team_id, ingest_id, owner_profile_id,
           jsonb_agg(jsonb_build_object(
               'assessment_id', assessment_id::text,
               'revision_number', revision_number,
               'normalized_response', normalized_response,
               'response_hash', response_hash,
               'provider_turns', provider_turns,
               'validated_at', validated_at
           ) ORDER BY revision_number, validated_at, assessment_id) AS response_history,
           max(revision_number) AS accepted_revision,
           max(provider_turns) AS provider_turns,
           max(model) AS model,
           max(tokenizer) AS tokenizer,
           max(input_tokens) AS input_tokens,
           max(output_tokens) AS output_tokens,
           max(candidate_context_tokens) AS candidate_context_tokens,
           bool_or(candidate_context_truncated) AS candidate_context_truncated,
           max(response_hash) AS response_hash,
           min(validated_at) AS validated_at
    FROM deduplicated
    GROUP BY team_id, ingest_id, owner_profile_id
)
INSERT INTO semantic_assessments (
    team_id, semantic_assessment_id, attempt_id, owner_profile_id,
    response_history, accepted_revision, provider_turns, model, tokenizer,
    input_tokens, output_tokens, candidate_context_tokens, candidate_context_truncated,
    response_hash, validated_at, created_at
)
SELECT grouped.team_id, gen_random_uuid(), grouped.ingest_id, grouped.owner_profile_id,
       grouped.response_history, NULLIF(grouped.accepted_revision, 0),
       grouped.provider_turns, grouped.model, grouped.tokenizer,
       grouped.input_tokens, grouped.output_tokens, grouped.candidate_context_tokens,
       COALESCE(grouped.candidate_context_truncated, false), grouped.response_hash,
       grouped.validated_at, COALESCE(grouped.validated_at, now())
FROM grouped
ON CONFLICT (team_id, attempt_id) DO NOTHING;

-- Preserve the intent rows that were used to activate source revisions and
-- evidence supersessions. They are no longer runtime workflow state, but
-- their immutable request lineage remains part of the terminal event history.
WITH intent_events AS (
    SELECT intent.team_id, intent.ingest_id AS attempt_id, intent.owner_profile_id,
           intent.created_at, 'legacy_source_revision_intent' AS event_kind,
           'recorded' AS outcome,
           jsonb_build_object(
               'source', 'remember_source_revision_intents',
               'legacy_intent_id', intent.intent_id,
               'fragment_id', intent.fragment_id,
               'source_key', intent.source_key,
               'source_kind', intent.source_kind,
               'authority', intent.authority,
               'revision_token', intent.revision_token,
               'expected_previous_revision_token', intent.expected_previous_revision_token,
               'content_hash', intent.content_hash,
               'envelope', intent.envelope,
               'source_id', intent.source_id,
               'source_revision_id', intent.source_revision_id,
               'space_id', intent.space_id,
               'space_generation', intent.space_generation
           ) AS metadata
    FROM remember_source_revision_intents AS intent
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = intent.team_id
     AND ingest.ingest_id = intent.ingest_id
     AND ingest.owner_profile_id = intent.owner_profile_id
    JOIN remember_attempts AS attempt
      ON attempt.team_id = intent.team_id
     AND attempt.attempt_id = intent.ingest_id
     AND attempt.owner_profile_id = intent.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
      AND attempt.submission_kind = 'remember'
    UNION ALL
    SELECT intent.team_id, intent.ingest_id AS attempt_id, intent.owner_profile_id,
           intent.created_at, 'legacy_supersession_intent' AS event_kind,
           'recorded' AS outcome,
           jsonb_build_object(
               'source', 'remember_supersession_intents',
               'legacy_intent_id', intent.intent_id,
               'fragment_id', intent.fragment_id,
               'target_fragment_id', intent.target_fragment_id,
               'space_id', intent.space_id,
               'space_generation', intent.space_generation
           ) AS metadata
    FROM remember_supersession_intents AS intent
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = intent.team_id
     AND ingest.ingest_id = intent.ingest_id
     AND ingest.owner_profile_id = intent.owner_profile_id
    JOIN remember_attempts AS attempt
      ON attempt.team_id = intent.team_id
     AND attempt.attempt_id = intent.ingest_id
     AND attempt.owner_profile_id = intent.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
      AND attempt.submission_kind = 'remember'
), numbered AS (
    SELECT intent_events.*,
           COALESCE((
               SELECT max(existing.sequence_no) + 1
               FROM remember_attempt_events AS existing
               WHERE existing.team_id = intent_events.team_id
                 AND existing.attempt_id = intent_events.attempt_id
           ), 0) + row_number() OVER (
               PARTITION BY team_id, attempt_id
               ORDER BY created_at, event_kind, metadata::text
           )::INTEGER - 1 AS sequence_no
    FROM intent_events
)
INSERT INTO remember_attempt_events (
    team_id, event_id, attempt_id, owner_profile_id, sequence_no,
    phase, event_kind, outcome, metadata, created_at
)
SELECT numbered.team_id, gen_random_uuid(), numbered.attempt_id,
       numbered.owner_profile_id, numbered.sequence_no,
       'legacy_cutover', numbered.event_kind, numbered.outcome,
       numbered.metadata, numbered.created_at
FROM numbered
WHERE NOT EXISTS (
    SELECT 1
    FROM remember_attempt_events AS existing
    WHERE existing.team_id = numbered.team_id
      AND existing.attempt_id = numbered.attempt_id
      AND existing.event_kind = numbered.event_kind
      AND existing.metadata ->> 'legacy_intent_id' = numbered.metadata ->> 'legacy_intent_id'
);

-- Do not copy raw request or assessor payloads into the new control-readable
-- artifact store; the source ID and hash preserve cutover lineage.

WITH payloads AS (
    SELECT payload.team_id, payload.quarantine_payload_id,
           payload.ingest_id AS attempt_id, payload.owner_profile_id,
           payload.quarantined_at,
           payload.quarantined_at + interval '7 days' AS retention_expires_at,
           convert_to(jsonb_build_object(
               'source_quarantine_payload_id', payload.quarantine_payload_id,
               'ingest_id', payload.ingest_id,
               'payload_sha256', payload.payload_sha256,
               'payload_status', 'redacted_on_cutover'
           )::text, 'UTF8') AS content_bytes
    FROM submission_quarantine_payloads AS payload
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = payload.team_id
     AND ingest.ingest_id = payload.ingest_id
     AND ingest.owner_profile_id = payload.owner_profile_id
    JOIN remember_attempts AS attempt
      ON attempt.team_id = payload.team_id
     AND attempt.attempt_id = payload.ingest_id
     AND attempt.owner_profile_id = payload.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
      AND attempt.submission_kind = 'remember'
      AND payload.quarantined_at >= transaction_timestamp() - interval '7 days'
)
INSERT INTO remember_failure_artifacts (
    team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
    content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at
)
SELECT payloads.team_id, payloads.quarantine_payload_id, payloads.attempt_id,
       payloads.owner_profile_id, 'legacy_submission_quarantine_metadata',
       'application/json', payloads.content_bytes, octet_length(payloads.content_bytes),
       'sha256:' || encode(digest(payloads.content_bytes, 'sha256'), 'hex'),
       payloads.quarantined_at, payloads.retention_expires_at
FROM payloads
ON CONFLICT (team_id, artifact_id) DO NOTHING;

WITH payload_events AS (
    SELECT payload.team_id, payload.ingest_id AS attempt_id, payload.owner_profile_id,
           payload.quarantined_at AS created_at,
           'legacy_quarantine_payload' AS event_kind, 'recorded' AS outcome,
           jsonb_build_object(
               'source', 'submission_quarantine_payloads',
               'legacy_payload_id', payload.quarantine_payload_id,
               'payload_sha256', payload.payload_sha256,
               'artifact_sha256', 'sha256:' || encode(digest(convert_to(jsonb_build_object(
                   'source_quarantine_payload_id', payload.quarantine_payload_id,
                   'ingest_id', payload.ingest_id,
                   'payload_sha256', payload.payload_sha256,
                   'payload_status', 'redacted_on_cutover'
               )::text, 'UTF8'), 'sha256'), 'hex'),
               'expires_at', payload.quarantined_at + interval '7 days'
           ) AS metadata
    FROM submission_quarantine_payloads AS payload
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = payload.team_id
     AND ingest.ingest_id = payload.ingest_id
     AND ingest.owner_profile_id = payload.owner_profile_id
    JOIN remember_attempts AS attempt
      ON attempt.team_id = payload.team_id
     AND attempt.attempt_id = payload.ingest_id
     AND attempt.owner_profile_id = payload.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
      AND attempt.submission_kind = 'remember'
), numbered AS (
    SELECT payload_events.*,
           COALESCE((
               SELECT max(existing.sequence_no) + 1
               FROM remember_attempt_events AS existing
               WHERE existing.team_id = payload_events.team_id
                 AND existing.attempt_id = payload_events.attempt_id
           ), 0) + row_number() OVER (
               PARTITION BY team_id, attempt_id
               ORDER BY created_at, event_kind, metadata::text
           )::INTEGER - 1 AS sequence_no
    FROM payload_events
)
INSERT INTO remember_attempt_events (
    team_id, event_id, attempt_id, owner_profile_id, sequence_no,
    phase, event_kind, outcome, metadata, created_at
)
SELECT numbered.team_id, gen_random_uuid(), numbered.attempt_id,
       numbered.owner_profile_id, numbered.sequence_no,
       'legacy_cutover', numbered.event_kind, numbered.outcome,
       numbered.metadata, numbered.created_at
FROM numbered
WHERE NOT EXISTS (
    SELECT 1
    FROM remember_attempt_events AS existing
    WHERE existing.team_id = numbered.team_id
      AND existing.attempt_id = numbered.attempt_id
      AND existing.event_kind = numbered.event_kind
      AND existing.metadata ->> 'legacy_payload_id' = numbered.metadata ->> 'legacy_payload_id'
);

WITH tombstone_events AS (
    SELECT tombstone.team_id, tombstone.ingest_id AS attempt_id, tombstone.owner_profile_id,
           tombstone.tombstoned_at AS created_at,
           'legacy_quarantine_tombstone' AS event_kind, 'recorded' AS outcome,
           jsonb_build_object(
               'source', 'submission_quarantine_tombstones',
               'fragment_id', tombstone.fragment_id,
               'content_hash', tombstone.content_hash,
               'tombstoned_at', tombstone.tombstoned_at
           ) AS metadata
    FROM submission_quarantine_tombstones AS tombstone
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = tombstone.team_id
     AND ingest.ingest_id = tombstone.ingest_id
     AND ingest.owner_profile_id = tombstone.owner_profile_id
    JOIN remember_attempts AS attempt
      ON attempt.team_id = tombstone.team_id
     AND attempt.attempt_id = tombstone.ingest_id
     AND attempt.owner_profile_id = tombstone.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
      AND attempt.submission_kind = 'remember'
), numbered AS (
    SELECT tombstone_events.*,
           COALESCE((
               SELECT max(existing.sequence_no) + 1
               FROM remember_attempt_events AS existing
               WHERE existing.team_id = tombstone_events.team_id
                 AND existing.attempt_id = tombstone_events.attempt_id
           ), 0) + row_number() OVER (
               PARTITION BY team_id, attempt_id
               ORDER BY created_at, event_kind, metadata::text
           )::INTEGER - 1 AS sequence_no
    FROM tombstone_events
)
INSERT INTO remember_attempt_events (
    team_id, event_id, attempt_id, owner_profile_id, sequence_no,
    phase, event_kind, outcome, metadata, created_at
)
SELECT numbered.team_id, gen_random_uuid(), numbered.attempt_id,
       numbered.owner_profile_id, numbered.sequence_no,
       'legacy_cutover', numbered.event_kind, numbered.outcome,
       numbered.metadata, numbered.created_at
FROM numbered
WHERE NOT EXISTS (
    SELECT 1
    FROM remember_attempt_events AS existing
    WHERE existing.team_id = numbered.team_id
      AND existing.attempt_id = numbered.attempt_id
      AND existing.event_kind = numbered.event_kind
      AND existing.metadata ->> 'fragment_id' = numbered.metadata ->> 'fragment_id'
);

-- Keep a temporary old-assessment to new-assessment map while all retained
-- assessment references are repointed. The old identifiers never survive as
-- runtime foreign keys.
CREATE TEMP TABLE dense_mem_semantic_assessment_map (
    team_id UUID NOT NULL,
    old_assessment_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    semantic_assessment_id UUID NOT NULL,
    PRIMARY KEY (team_id, old_assessment_id)
) ON COMMIT DROP;

INSERT INTO dense_mem_semantic_assessment_map (team_id, old_assessment_id, ingest_id, semantic_assessment_id)
SELECT DISTINCT map.team_id, (entry.value ->> 'assessment_id')::uuid,
       map.attempt_id, map.semantic_assessment_id
FROM semantic_assessments AS map
CROSS JOIN LATERAL jsonb_array_elements(map.response_history) AS entry(value)
WHERE NULLIF(entry.value ->> 'assessment_id', '') IS NOT NULL
ON CONFLICT (team_id, old_assessment_id) DO NOTHING;

DO $$
DECLARE
    source_assessments BIGINT;
    mapped_assessments BIGINT;
BEGIN
    SELECT count(*) INTO source_assessments
    FROM dense_mem_semantic_assessment_map;
    SELECT COALESCE(count(DISTINCT entry.value ->> 'assessment_id'), 0)
      INTO mapped_assessments
    FROM semantic_assessments
    CROSS JOIN LATERAL jsonb_array_elements(response_history) AS entry(value);
    IF source_assessments <> mapped_assessments THEN
        RAISE EXCEPTION 'synchronous Remember assessment history count mismatch: % source rows, % mapped rows', source_assessments, mapped_assessments;
    END IF;
END;
$$;

-- Synchronous Remember commits retain canonical ingest and semantic-assessment
-- identity but do not manufacture placement-run/item rows. Repoint every
-- retained assessment reference before the retired assessment table disappears.
ALTER TABLE predicate_registration_events
    ADD COLUMN IF NOT EXISTS ingest_id UUID;

DROP TRIGGER IF EXISTS predicate_registration_events_append_only ON predicate_registration_events;

-- The reassignment below changes the referenced assessment identifiers. Remove
-- the old placement-assessment foreign keys before updating the identifiers;
-- the new semantic-assessment constraints are installed after all mappings are
-- verified.
ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_assessment_ref;
ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_assessment_ref;
ALTER TABLE review_tasks
    DROP CONSTRAINT IF EXISTS review_tasks_assessment_ref;
ALTER TABLE predicate_registration_events
    DROP CONSTRAINT IF EXISTS predicate_registration_events_team_id_assessment_id_fkey,
    DROP CONSTRAINT IF EXISTS predicate_registration_events_assessment_id_fkey,
    DROP CONSTRAINT IF EXISTS predicate_registration_events_assessment_ref;

-- A legacy predicate-registration event can outlive its placement-run link
-- while its assessment history still identifies one authoritative ingest.
-- Preserve both legacy identifiers and recover that mapping without deleting
-- the append-only event.
UPDATE predicate_registration_events AS event
SET metadata = event.metadata || jsonb_build_object(
    'legacy_placement_run_id', event.placement_run_id,
    'legacy_assessment_id', event.assessment_id
);

UPDATE predicate_registration_events AS event
SET ingest_id = run.ingest_id
FROM placement_runs AS run
WHERE run.team_id = event.team_id
  AND run.placement_run_id = event.placement_run_id
  AND run.owner_profile_id = event.owner_profile_id
  AND event.ingest_id IS NULL;

UPDATE predicate_registration_events AS event
SET ingest_id = assessment_map.ingest_id
FROM dense_mem_semantic_assessment_map AS assessment_map,
     semantic_assessments AS assessment
WHERE assessment_map.team_id = event.team_id
  AND assessment_map.old_assessment_id = event.assessment_id
  AND assessment.team_id = assessment_map.team_id
  AND assessment.semantic_assessment_id = assessment_map.semantic_assessment_id
  AND assessment.owner_profile_id = event.owner_profile_id
  AND event.ingest_id IS NULL;

DO $$
DECLARE
    missing_ingest BIGINT;
    missing_assessments BIGINT;
    unknown_origins BIGINT;
    conflicting_mappings BIGINT;
BEGIN
    SELECT count(*) INTO missing_ingest
    FROM predicate_registration_events
    WHERE ingest_id IS NULL;
    IF missing_ingest <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % predicate registration events have no ingest mapping', missing_ingest;
    END IF;

    SELECT count(*) INTO conflicting_mappings
    FROM predicate_registration_events AS event
    LEFT JOIN dense_mem_semantic_assessment_map AS assessment_map
      ON assessment_map.team_id = event.team_id
     AND assessment_map.old_assessment_id = event.assessment_id
    LEFT JOIN semantic_assessments AS assessment
      ON assessment.team_id = assessment_map.team_id
     AND assessment.semantic_assessment_id = assessment_map.semantic_assessment_id
    WHERE assessment_map.semantic_assessment_id IS NULL
       OR assessment.owner_profile_id IS DISTINCT FROM event.owner_profile_id
       OR assessment_map.ingest_id IS DISTINCT FROM event.ingest_id;
    IF conflicting_mappings <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % predicate registration event mappings disagree with assessment history', conflicting_mappings;
    END IF;

    SELECT count(*) INTO unknown_origins
    FROM predicate_registration_events AS event
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = event.team_id
     AND ingest.ingest_id = event.ingest_id
     AND ingest.owner_profile_id = event.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_origins <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % predicate registrations have an unknown origin', unknown_origins;
    END IF;

    SELECT count(*) INTO missing_assessments
    FROM (
        SELECT team_id, assessment_id FROM entity_resolution_events WHERE assessment_id IS NOT NULL
        UNION ALL
        SELECT team_id, assessment_id FROM verification_events WHERE assessment_id IS NOT NULL
        UNION ALL
        SELECT team_id, assessment_id FROM review_tasks WHERE assessment_id IS NOT NULL
        UNION ALL
        SELECT team_id, assessment_id FROM predicate_registration_events WHERE assessment_id IS NOT NULL
    ) AS retained
    LEFT JOIN dense_mem_semantic_assessment_map AS map
      ON map.team_id = retained.team_id
     AND map.old_assessment_id = retained.assessment_id
    WHERE map.semantic_assessment_id IS NULL;
    IF missing_assessments <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % retained assessment references are unmapped', missing_assessments;
    END IF;
END;
$$;

UPDATE entity_resolution_events AS event
SET assessment_id = map.semantic_assessment_id
FROM dense_mem_semantic_assessment_map AS map
WHERE map.team_id = event.team_id
  AND map.old_assessment_id = event.assessment_id;

UPDATE verification_events AS event
SET assessment_id = map.semantic_assessment_id
FROM dense_mem_semantic_assessment_map AS map
WHERE map.team_id = event.team_id
  AND map.old_assessment_id = event.assessment_id;

UPDATE review_tasks AS task
SET assessment_id = map.semantic_assessment_id
FROM dense_mem_semantic_assessment_map AS map
WHERE map.team_id = task.team_id
  AND map.old_assessment_id = task.assessment_id;

UPDATE predicate_registration_events AS event
SET assessment_id = map.semantic_assessment_id
FROM dense_mem_semantic_assessment_map AS map
WHERE map.team_id = event.team_id
  AND map.old_assessment_id = event.assessment_id;

DROP INDEX IF EXISTS submission_relationship_results_submission_idx;
ALTER TABLE submission_relationship_results
    DROP CONSTRAINT IF EXISTS submission_relationship_results_ref_unique,
    DROP COLUMN IF EXISTS placement_run_id;

ALTER TABLE submission_relationship_results
    ADD CONSTRAINT submission_relationship_results_ingest_ref_unique
    UNIQUE (team_id, ingest_id, relationship_ref, owner_profile_id);

ALTER TABLE predicate_registration_events
    ALTER COLUMN ingest_id SET NOT NULL,
    ALTER COLUMN assessment_id SET NOT NULL;

ALTER TABLE predicate_registration_events
    DROP CONSTRAINT IF EXISTS predicate_registration_events_placement_run_id_fkey,
    DROP CONSTRAINT IF EXISTS predicate_registration_events_team_id_assessment_id_fkey,
    DROP CONSTRAINT IF EXISTS predicate_registration_events_assessment_id_fkey,
    DROP CONSTRAINT IF EXISTS predicate_registration_events_placement_run_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS predicate_registration_events_ingest_ref_fkey,
    DROP CONSTRAINT IF EXISTS predicate_registration_events_assessment_ref_fkey;

ALTER TABLE predicate_registration_events
    DROP CONSTRAINT IF EXISTS predicate_registration_events_placement_run_id_relationship_ref_key;

ALTER TABLE predicate_registration_events
    DROP COLUMN IF EXISTS placement_run_id;

ALTER TABLE predicate_registration_events
    ADD CONSTRAINT predicate_registration_events_ingest_ref_unique
    UNIQUE (team_id, ingest_id, relationship_ref),
    ADD CONSTRAINT predicate_registration_events_ingest_ref_fkey
        FOREIGN KEY (team_id, ingest_id, owner_profile_id)
        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    ADD CONSTRAINT predicate_registration_events_assessment_ref_fkey
        FOREIGN KEY (team_id, assessment_id)
        REFERENCES semantic_assessments(team_id, semantic_assessment_id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS predicate_registration_events_assessment_created_idx
    ON predicate_registration_events(team_id, ingest_id, created_at ASC, predicate_registration_event_id);

CREATE TRIGGER predicate_registration_events_append_only
    BEFORE UPDATE OR DELETE ON predicate_registration_events
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- +goose StatementEnd

-- +goose StatementBegin

-- A stopped-service cutover cannot leave a correction waiting on a confirmation
-- token issued by the retired request workflow. Preserve the submission row,
-- but make the outcome terminal and provide the normal retry-correction path.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('lock_timeout', '30s', true);

UPDATE relationship_correction_submissions
SET processing_state = 'failed',
    confirmation_token = '',
    confirmation_expires_at = NULL,
    candidates = '[]'::jsonb,
    selection = '{}'::jsonb,
    error_code = 'contract_superseded',
    error_message = 'The v2.6.1 synchronous correction contract superseded this pending confirmation; retry the correction.',
    completed_at = COALESCE(completed_at, now()),
    updated_at = now()
WHERE processing_state IN ('processing', 'awaiting_confirmation');

-- +goose StatementEnd

-- +goose StatementBegin

-- Operationally failed Remember attempts may be retried with the same
-- idempotency key. Keep successful domain outcomes canonical while allowing a
-- new ingest/run to replace an older failed attempt without rewriting its
-- append-only history.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('lock_timeout', '30s', true);

UPDATE knowledge_ingests AS ingest
SET status = 'failed',
    error = COALESCE(NULLIF(run.error, ''), ingest.error),
    completed_at = COALESCE(ingest.completed_at, run.completed_at, now()),
    updated_at = now()
FROM placement_runs AS run
WHERE run.team_id = ingest.team_id
  AND run.ingest_id = ingest.ingest_id
  AND run.owner_profile_id = ingest.owner_profile_id
  AND run.status = 'failed'
  AND ingest.status IN ('queued', 'guarded', 'processing');

DROP INDEX IF EXISTS knowledge_ingests_idempotency_unique;
CREATE UNIQUE INDEX knowledge_ingests_idempotency_unique
    ON knowledge_ingests(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '' AND status <> 'failed';

-- +goose StatementEnd

-- +goose StatementBegin

-- Hard cutover gate. The synchronous binary must never boot against in-flight
-- placement or job work, and every Remember-origin row must have a terminal
-- attempt projection before the retired tables are removed.
DO $$
DECLARE
    active_count BIGINT;
    job_count BIGINT;
    drifted_document_count BIGINT;
    source_count BIGINT;
    attempt_count BIGINT;
    source_hash TEXT;
    attempt_hash TEXT;
    unknown_count BIGINT;
    item_source_count BIGINT;
    item_event_count BIGINT;
    outcome_source_count BIGINT;
    outcome_event_count BIGINT;
    assessment_source_count BIGINT;
    assessment_history_count BIGINT;
    item_source_hash TEXT;
    item_event_hash TEXT;
    outcome_source_hash TEXT;
    outcome_event_hash TEXT;
    assessment_source_hash TEXT;
    assessment_history_hash TEXT;
    source_intent_count BIGINT;
    source_intent_event_count BIGINT;
    source_intent_hash TEXT;
    source_intent_event_hash TEXT;
    supersession_intent_count BIGINT;
    supersession_intent_event_count BIGINT;
    supersession_intent_hash TEXT;
    supersession_intent_event_hash TEXT;
    quarantine_payload_count BIGINT;
    quarantine_payload_source_count BIGINT;
    quarantine_payload_event_count BIGINT;
    quarantine_artifact_count BIGINT;
    quarantine_payload_hash TEXT;
    quarantine_payload_source_hash TEXT;
    quarantine_payload_event_hash TEXT;
    quarantine_artifact_hash TEXT;
    quarantine_tombstone_count BIGINT;
    quarantine_tombstone_event_count BIGINT;
    quarantine_tombstone_hash TEXT;
    quarantine_tombstone_event_hash TEXT;
    correction_superseded_count BIGINT;
    retired_oids OID[];
    fk RECORD;
    retired_table TEXT;
BEGIN
    SELECT count(*) INTO unknown_count
    FROM placement_runs AS run
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = run.team_id
     AND ingest.ingest_id = run.ingest_id
     AND ingest.owner_profile_id = run.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % placement runs have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM placement_items AS item
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = item.team_id
     AND ingest.ingest_id = item.ingest_id
     AND ingest.owner_profile_id = item.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % placement items have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM placement_outcomes AS outcome
    JOIN placement_runs AS run
      ON run.team_id = outcome.team_id
     AND run.placement_run_id = outcome.placement_run_id
     AND run.owner_profile_id = outcome.owner_profile_id
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = run.team_id
     AND ingest.ingest_id = run.ingest_id
     AND ingest.owner_profile_id = run.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % placement outcomes have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM placement_assessments AS assessment
    WHERE NOT (
        (
            assessment.assessment_scope = 'submission'
            AND EXISTS (
                SELECT 1
                FROM knowledge_ingests AS ingest
                WHERE ingest.team_id = assessment.team_id
                  AND ingest.ingest_id = assessment.ingest_id
                  AND ingest.owner_profile_id = assessment.owner_profile_id
                  AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
            )
        )
        OR (
            assessment.assessment_scope = 'item'
            AND EXISTS (
                SELECT 1
                FROM placement_items AS item
                JOIN knowledge_ingests AS ingest
                  ON ingest.team_id = item.team_id
                 AND ingest.ingest_id = item.ingest_id
                 AND ingest.owner_profile_id = item.owner_profile_id
                WHERE item.team_id = assessment.team_id
                  AND item.placement_item_id = assessment.placement_item_id
                  AND item.owner_profile_id = assessment.owner_profile_id
                  AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
            )
        )
    );
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % placement assessments have an unknown or unmapped origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM submission_assessment_response_revisions AS revision
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = revision.team_id
     AND ingest.ingest_id = revision.ingest_id
     AND ingest.owner_profile_id = revision.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % assessment response revisions have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM remember_source_revision_intents AS intent
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = intent.team_id
     AND ingest.ingest_id = intent.ingest_id
     AND ingest.owner_profile_id = intent.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % source revision intents have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM remember_supersession_intents AS intent
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = intent.team_id
     AND ingest.ingest_id = intent.ingest_id
     AND ingest.owner_profile_id = intent.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % supersession intents have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM submission_quarantine_payloads AS payload
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = payload.team_id
     AND ingest.ingest_id = payload.ingest_id
     AND ingest.owner_profile_id = payload.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % quarantine payloads have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM submission_quarantine_tombstones AS tombstone
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = tombstone.team_id
     AND ingest.ingest_id = tombstone.ingest_id
     AND ingest.owner_profile_id = tombstone.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % quarantine tombstones have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO unknown_count
    FROM submission_relationship_results AS result
    LEFT JOIN knowledge_ingests AS ingest
      ON ingest.team_id = result.team_id
     AND ingest.ingest_id = result.ingest_id
     AND ingest.owner_profile_id = result.owner_profile_id
    WHERE ingest.ingest_id IS NULL
       OR ingest.metadata ->> '_dense_mem_telemetry_origin' IS DISTINCT FROM 'remember';
    IF unknown_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % relationship results have an unknown origin', unknown_count;
    END IF;

    SELECT count(*) INTO active_count
    FROM placement_runs
    WHERE status IN ('queued', 'guarded', 'processing');
    IF active_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % placement runs are still active', active_count;
    END IF;

    SELECT count(*) INTO job_count
    FROM embedding_jobs
    WHERE status IN ('queued', 'processing');
    IF job_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % embedding jobs are still active', job_count;
    END IF;

    SELECT count(*) INTO drifted_document_count
    FROM search_documents AS document
    JOIN search_index_generations AS generation
      ON generation.embedding_contract_id = document.embedding_contract_id
     AND generation.embedding_dimensions = document.embedding_dimensions
     AND generation.activation_state = 'active'
    JOIN embedding_contracts AS contract
      ON contract.embedding_contract_id = document.embedding_contract_id
     AND contract.dimensions = document.embedding_dimensions
     AND contract.lifecycle_state = 'active'
    WHERE document.search_state <> 'not_required'
      AND (
          document.search_state <> 'current'
          OR document.embedding IS NULL
          OR vector_dims(document.embedding) <> document.embedding_dimensions
      );
    IF drifted_document_count <> 0 THEN
        RAISE EXCEPTION 'synchronous Remember cutover blocked: % active-contract search documents are not current', drifted_document_count;
    END IF;

    SELECT count(*) INTO source_count
    FROM knowledge_ingests
    WHERE metadata ->> '_dense_mem_telemetry_origin' = 'remember';
    SELECT count(*) INTO attempt_count
    FROM remember_attempts AS attempt
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = attempt.team_id
     AND ingest.ingest_id = attempt.attempt_id
     AND ingest.owner_profile_id = attempt.owner_profile_id
    WHERE attempt.submission_kind = 'remember'
      AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';
    IF source_count <> attempt_count THEN
        RAISE EXCEPTION 'synchronous Remember cutover count mismatch: % source ingests, % attempts', source_count, attempt_count;
    END IF;

    SELECT encode(digest(COALESCE(string_agg(ingest_id::text || ':' || request_hash, '|' ORDER BY ingest_id), ''), 'sha256'), 'hex')
      INTO source_hash
    FROM knowledge_ingests
    WHERE metadata ->> '_dense_mem_telemetry_origin' = 'remember';
    SELECT encode(digest(COALESCE(string_agg(attempt.attempt_id::text || ':' || attempt.request_hash, '|' ORDER BY attempt.attempt_id), ''), 'sha256'), 'hex')
      INTO attempt_hash
    FROM remember_attempts AS attempt
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = attempt.team_id
     AND ingest.ingest_id = attempt.attempt_id
     AND ingest.owner_profile_id = attempt.owner_profile_id
    WHERE attempt.submission_kind = 'remember'
      AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';
    IF source_hash IS DISTINCT FROM attempt_hash THEN
        RAISE EXCEPTION 'synchronous Remember cutover hash mismatch: source %, attempts %', source_hash, attempt_hash;
    END IF;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               item.team_id::text || ':' || item.placement_item_id::text || ':' ||
	               item.status || ':' || item.category || ':' ||
	               CASE WHEN btrim(item.error) = '' THEN '' ELSE 'sha256:' || encode(digest(item.error, 'sha256'), 'hex') END || ':' ||
	               'sha256:' || encode(digest(COALESCE(item.result::text, '{}'), 'sha256'), 'hex'),
               '|' ORDER BY item.team_id, item.placement_item_id), ''), 'sha256'), 'hex')
      INTO item_source_count, item_source_hash
    FROM placement_items AS item
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = item.team_id
     AND ingest.ingest_id = item.ingest_id
     AND ingest.owner_profile_id = item.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';
    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               event.team_id::text || ':' ||
               (event.metadata ->> 'legacy_item_id') || ':' ||
               (event.metadata ->> 'status') || ':' ||
               (event.metadata ->> 'category') || ':' ||
	               (event.metadata ->> 'error_sha256') || ':' ||
	               (event.metadata ->> 'result_sha256'),
               '|' ORDER BY event.team_id, event.metadata ->> 'legacy_item_id'), ''), 'sha256'), 'hex')
      INTO item_event_count, item_event_hash
    FROM remember_attempt_events AS event
    WHERE event.event_kind = 'legacy_item';
    IF item_source_count <> item_event_count OR item_source_hash IS DISTINCT FROM item_event_hash THEN
        RAISE EXCEPTION 'synchronous Remember placement-item history mismatch: source count/hash %/% events %/%',
            item_source_count, item_source_hash, item_event_count, item_event_hash;
    END IF;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               run.team_id::text || ':' || outcome.outcome_id::text || ':' ||
	               outcome.outcome_kind || ':' || outcome.status || ':' ||
	               'sha256:' || encode(digest(COALESCE(outcome.payload::text, '{}'), 'sha256'), 'hex'),
               '|' ORDER BY run.team_id, outcome.outcome_id), ''), 'sha256'), 'hex')
      INTO outcome_source_count, outcome_source_hash
    FROM placement_outcomes AS outcome
    JOIN placement_runs AS run
      ON run.team_id = outcome.team_id
     AND run.placement_run_id = outcome.placement_run_id
     AND run.owner_profile_id = outcome.owner_profile_id
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = run.team_id
     AND ingest.ingest_id = run.ingest_id
     AND ingest.owner_profile_id = run.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';
    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               event.team_id::text || ':' ||
               (event.metadata ->> 'legacy_outcome_id') || ':' ||
               (event.metadata ->> 'outcome_kind') || ':' ||
	               (event.metadata ->> 'status') || ':' ||
	               (event.metadata ->> 'payload_sha256'),
               '|' ORDER BY event.team_id, event.metadata ->> 'legacy_outcome_id'), ''), 'sha256'), 'hex')
      INTO outcome_event_count, outcome_event_hash
    FROM remember_attempt_events AS event
    WHERE event.event_kind = 'legacy_outcome';
    IF outcome_source_count <> outcome_event_count OR outcome_source_hash IS DISTINCT FROM outcome_event_hash THEN
        RAISE EXCEPTION 'synchronous Remember placement-outcome history mismatch: source count/hash %/% events %/%',
            outcome_source_count, outcome_source_hash, outcome_event_count, outcome_event_hash;
    END IF;

    WITH source_rows AS (
        SELECT assessment.team_id,
               assessment.assessment_id,
               0 AS revision_number,
               assessment.response_hash
        FROM placement_assessments AS assessment
        WHERE (
            assessment.assessment_scope = 'submission'
            AND EXISTS (
                SELECT 1 FROM knowledge_ingests AS ingest
                WHERE ingest.team_id = assessment.team_id
                  AND ingest.ingest_id = assessment.ingest_id
                  AND ingest.owner_profile_id = assessment.owner_profile_id
                  AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
            )
        ) OR (
            assessment.assessment_scope = 'item'
            AND EXISTS (
                SELECT 1
                FROM placement_items AS item
                JOIN knowledge_ingests AS ingest
                  ON ingest.team_id = item.team_id
                 AND ingest.ingest_id = item.ingest_id
                 AND ingest.owner_profile_id = item.owner_profile_id
                WHERE item.team_id = assessment.team_id
                  AND item.placement_item_id = assessment.placement_item_id
                  AND item.owner_profile_id = assessment.owner_profile_id
                  AND ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
            )
        )
        UNION ALL
        SELECT revision.team_id, revision.assessment_id,
               revision.revision_number, revision.response_hash
        FROM submission_assessment_response_revisions AS revision
        JOIN knowledge_ingests AS ingest
          ON ingest.team_id = revision.team_id
         AND ingest.ingest_id = revision.ingest_id
         AND ingest.owner_profile_id = revision.owner_profile_id
        WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
    )
    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               source_rows.team_id::text || ':' || source_rows.assessment_id::text || ':' ||
               source_rows.revision_number::text || ':' || source_rows.response_hash,
               '|' ORDER BY source_rows.team_id, source_rows.assessment_id, source_rows.revision_number), ''), 'sha256'), 'hex')
      INTO assessment_source_count, assessment_source_hash
    FROM source_rows;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               assessment.team_id::text || ':' ||
               (entry.value ->> 'assessment_id') || ':' ||
               (entry.value ->> 'revision_number') || ':' ||
               (entry.value ->> 'response_hash'),
               '|' ORDER BY assessment.team_id,
                            entry.value ->> 'assessment_id',
                            (entry.value ->> 'revision_number')::integer), ''), 'sha256'), 'hex')
      INTO assessment_history_count, assessment_history_hash
    FROM semantic_assessments AS assessment
    CROSS JOIN LATERAL jsonb_array_elements(assessment.response_history) AS entry(value);
    IF assessment_source_count <> assessment_history_count OR assessment_source_hash IS DISTINCT FROM assessment_history_hash THEN
        RAISE EXCEPTION 'synchronous Remember assessment history mismatch: source count/hash %/% history %/%',
            assessment_source_count, assessment_source_hash, assessment_history_count, assessment_history_hash;
    END IF;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               intent.team_id::text || ':' || intent.intent_id::text || ':' ||
               intent.ingest_id::text || ':' || intent.fragment_id::text || ':' ||
               intent.source_key || ':' || intent.source_kind || ':' || intent.authority || ':' ||
               intent.revision_token || ':' || intent.expected_previous_revision_token || ':' ||
               intent.content_hash || ':' || COALESCE(intent.envelope::text, '{}') || ':' ||
               COALESCE(intent.source_id::text, '') || ':' ||
               COALESCE(intent.source_revision_id::text, ''),
               '|' ORDER BY intent.team_id, intent.intent_id), ''), 'sha256'), 'hex')
      INTO source_intent_count, source_intent_hash
    FROM remember_source_revision_intents AS intent
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = intent.team_id
     AND ingest.ingest_id = intent.ingest_id
     AND ingest.owner_profile_id = intent.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               event.team_id::text || ':' ||
               (event.metadata ->> 'legacy_intent_id') || ':' ||
               event.attempt_id::text || ':' ||
               (event.metadata ->> 'fragment_id') || ':' ||
               (event.metadata ->> 'source_key') || ':' ||
               (event.metadata ->> 'source_kind') || ':' ||
               (event.metadata ->> 'authority') || ':' ||
               (event.metadata ->> 'revision_token') || ':' ||
               (event.metadata ->> 'expected_previous_revision_token') || ':' ||
               (event.metadata ->> 'content_hash') || ':' ||
               COALESCE((event.metadata -> 'envelope')::text, '{}') || ':' ||
               COALESCE(event.metadata ->> 'source_id', '') || ':' ||
               COALESCE(event.metadata ->> 'source_revision_id', ''),
               '|' ORDER BY event.team_id, event.metadata ->> 'legacy_intent_id'), ''), 'sha256'), 'hex')
      INTO source_intent_event_count, source_intent_event_hash
    FROM remember_attempt_events AS event
    WHERE event.event_kind = 'legacy_source_revision_intent';
    IF source_intent_count <> source_intent_event_count OR source_intent_hash IS DISTINCT FROM source_intent_event_hash THEN
        RAISE EXCEPTION 'synchronous Remember source-revision intent history mismatch: source count/hash %/% events %/%',
            source_intent_count, source_intent_hash, source_intent_event_count, source_intent_event_hash;
    END IF;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               intent.team_id::text || ':' || intent.intent_id::text || ':' ||
               intent.ingest_id::text || ':' || intent.fragment_id::text || ':' ||
               intent.target_fragment_id::text || ':' || COALESCE(intent.space_id::text, '') || ':' ||
               COALESCE(intent.space_generation::text, ''),
               '|' ORDER BY intent.team_id, intent.intent_id), ''), 'sha256'), 'hex')
      INTO supersession_intent_count, supersession_intent_hash
    FROM remember_supersession_intents AS intent
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = intent.team_id
     AND ingest.ingest_id = intent.ingest_id
     AND ingest.owner_profile_id = intent.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               event.team_id::text || ':' ||
               (event.metadata ->> 'legacy_intent_id') || ':' ||
               event.attempt_id::text || ':' ||
               (event.metadata ->> 'fragment_id') || ':' ||
               (event.metadata ->> 'target_fragment_id') || ':' ||
               COALESCE(event.metadata ->> 'space_id', '') || ':' ||
               COALESCE(event.metadata ->> 'space_generation', ''),
               '|' ORDER BY event.team_id, event.metadata ->> 'legacy_intent_id'), ''), 'sha256'), 'hex')
      INTO supersession_intent_event_count, supersession_intent_event_hash
    FROM remember_attempt_events AS event
    WHERE event.event_kind = 'legacy_supersession_intent';
    IF supersession_intent_count <> supersession_intent_event_count OR supersession_intent_hash IS DISTINCT FROM supersession_intent_event_hash THEN
        RAISE EXCEPTION 'synchronous Remember supersession intent history mismatch: source count/hash %/% events %/%',
            supersession_intent_count, supersession_intent_hash, supersession_intent_event_count, supersession_intent_event_hash;
    END IF;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               payload.team_id::text || ':' || payload.quarantine_payload_id::text || ':' ||
               payload.payload_sha256 || ':' ||
               ('sha256:' || encode(digest(convert_to(jsonb_build_object(
                   'source_quarantine_payload_id', payload.quarantine_payload_id,
                   'ingest_id', payload.ingest_id,
                   'payload_sha256', payload.payload_sha256,
                   'payload_status', 'redacted_on_cutover'
               )::text, 'UTF8'), 'sha256'), 'hex')),
               '|' ORDER BY payload.team_id, payload.quarantine_payload_id), ''), 'sha256'), 'hex')
      INTO quarantine_payload_count, quarantine_payload_hash
    FROM submission_quarantine_payloads AS payload
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = payload.team_id
     AND ingest.ingest_id = payload.ingest_id
     AND ingest.owner_profile_id = payload.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
      AND payload.quarantined_at >= transaction_timestamp() - interval '7 days';

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               artifact.team_id::text || ':' || artifact.artifact_id::text || ':' ||
               COALESCE(convert_from(artifact.content_bytes, 'UTF8')::jsonb ->> 'payload_sha256', '') || ':' ||
               artifact.content_sha256,
               '|' ORDER BY artifact.team_id, artifact.artifact_id), ''), 'sha256'), 'hex')
      INTO quarantine_artifact_count, quarantine_artifact_hash
    FROM remember_failure_artifacts AS artifact
    WHERE artifact.artifact_kind = 'legacy_submission_quarantine_metadata';
    IF quarantine_payload_count <> quarantine_artifact_count OR quarantine_payload_hash IS DISTINCT FROM quarantine_artifact_hash THEN
        RAISE EXCEPTION 'synchronous Remember quarantine payload history mismatch: source count/hash %/% artifacts %/%',
            quarantine_payload_count, quarantine_payload_hash, quarantine_artifact_count, quarantine_artifact_hash;
    END IF;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               payload.team_id::text || ':' || payload.quarantine_payload_id::text || ':' ||
               payload.payload_sha256 || ':' ||
               ('sha256:' || encode(digest(convert_to(jsonb_build_object(
                   'source_quarantine_payload_id', payload.quarantine_payload_id,
                   'ingest_id', payload.ingest_id,
                   'payload_sha256', payload.payload_sha256,
                   'payload_status', 'redacted_on_cutover'
               )::text, 'UTF8'), 'sha256'), 'hex')),
               '|' ORDER BY payload.team_id, payload.quarantine_payload_id), ''), 'sha256'), 'hex')
      INTO quarantine_payload_source_count, quarantine_payload_source_hash
    FROM submission_quarantine_payloads AS payload
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = payload.team_id
     AND ingest.ingest_id = payload.ingest_id
     AND ingest.owner_profile_id = payload.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               event.team_id::text || ':' ||
               (event.metadata ->> 'legacy_payload_id') || ':' ||
               COALESCE(event.metadata ->> 'payload_sha256', '') || ':' ||
               COALESCE(event.metadata ->> 'artifact_sha256', ''),
               '|' ORDER BY event.team_id, event.metadata ->> 'legacy_payload_id'), ''), 'sha256'), 'hex')
      INTO quarantine_payload_event_count, quarantine_payload_event_hash
    FROM remember_attempt_events AS event
    WHERE event.event_kind = 'legacy_quarantine_payload';
    IF quarantine_payload_source_count <> quarantine_payload_event_count OR quarantine_payload_source_hash IS DISTINCT FROM quarantine_payload_event_hash THEN
        RAISE EXCEPTION 'synchronous Remember quarantine payload event history mismatch: source count/hash %/% events %/%',
            quarantine_payload_source_count, quarantine_payload_source_hash, quarantine_payload_event_count, quarantine_payload_event_hash;
    END IF;

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               tombstone.team_id::text || ':' || tombstone.fragment_id::text || ':' ||
               tombstone.ingest_id::text || ':' || tombstone.owner_profile_id::text || ':' ||
               tombstone.content_hash,
               '|' ORDER BY tombstone.team_id, tombstone.fragment_id), ''), 'sha256'), 'hex')
      INTO quarantine_tombstone_count, quarantine_tombstone_hash
    FROM submission_quarantine_tombstones AS tombstone
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = tombstone.team_id
     AND ingest.ingest_id = tombstone.ingest_id
     AND ingest.owner_profile_id = tombstone.owner_profile_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember';

    SELECT count(*),
           encode(digest(COALESCE(string_agg(
               event.team_id::text || ':' ||
               (event.metadata ->> 'fragment_id') || ':' ||
               event.attempt_id::text || ':' ||
               event.owner_profile_id::text || ':' ||
               (event.metadata ->> 'content_hash'),
               '|' ORDER BY event.team_id, event.metadata ->> 'fragment_id'), ''), 'sha256'), 'hex')
      INTO quarantine_tombstone_event_count, quarantine_tombstone_event_hash
    FROM remember_attempt_events AS event
    WHERE event.event_kind = 'legacy_quarantine_tombstone';
    IF quarantine_tombstone_count <> quarantine_tombstone_event_count OR quarantine_tombstone_hash IS DISTINCT FROM quarantine_tombstone_event_hash THEN
        RAISE EXCEPTION 'synchronous Remember quarantine tombstone history mismatch: source count/hash %/% events %/%',
            quarantine_tombstone_count, quarantine_tombstone_hash, quarantine_tombstone_event_count, quarantine_tombstone_event_hash;
    END IF;

    SELECT count(*) INTO correction_superseded_count
    FROM relationship_correction_submissions
    WHERE error_code = 'contract_superseded'
      AND error_message LIKE 'The v2.6.1 synchronous correction contract superseded%';

    -- Remove every retained foreign key that points into a retired table. The
    -- drop below intentionally omits CASCADE so any unrecognised dependency
    -- aborts the migration instead of silently deleting it.
    SELECT array_agg(c.oid)
      INTO retired_oids
    FROM pg_class AS c
    JOIN pg_namespace AS n ON n.oid = c.relnamespace
    WHERE n.nspname = current_schema()
      AND c.relname = ANY(ARRAY[
          'placement_runs', 'placement_items', 'placement_outcomes',
          'placement_assessments', 'embedding_jobs',
          'remember_source_revision_intents', 'remember_supersession_intents',
          'submission_assessment_response_revisions',
          'submission_quarantine_payloads', 'submission_quarantine_tombstones'
      ]);
    IF retired_oids IS NOT NULL THEN
        FOR fk IN
            SELECT conrelid::regclass AS relation_name, conname
            FROM pg_constraint
            WHERE contype = 'f' AND confrelid = ANY(retired_oids)
        LOOP
            EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', fk.relation_name, fk.conname);
        END LOOP;
    END IF;

    -- Retained semantic rows carry ingest/assessment lineage only. Remove the
    -- nullable placement item columns after their foreign keys are gone.
    IF to_regclass('relationship_observations') IS NOT NULL THEN
        ALTER TABLE relationship_observations DROP COLUMN IF EXISTS placement_item_id;
    END IF;
    IF to_regclass('entity_resolution_events') IS NOT NULL THEN
        ALTER TABLE entity_resolution_events DROP COLUMN IF EXISTS placement_item_id;
    END IF;
    IF to_regclass('review_tasks') IS NOT NULL THEN
        ALTER TABLE review_tasks DROP COLUMN IF EXISTS placement_item_id;
    END IF;

    -- Reconciliation history is document-centric after the cutover. Preserve
    -- the durable run record but remove queue-specific names and columns.
    IF to_regclass('embedding_reconciliation_runs') IS NOT NULL
       AND to_regclass('search_reconciliation_runs') IS NULL THEN
        ALTER TABLE embedding_reconciliation_runs RENAME TO search_reconciliation_runs;
    END IF;
    IF to_regclass('search_reconciliation_runs') IS NOT NULL THEN
        IF EXISTS (SELECT 1 FROM information_schema.columns AS column_info WHERE column_info.table_name = 'search_reconciliation_runs' AND column_info.column_name = 'requeued_count') THEN
            ALTER TABLE search_reconciliation_runs RENAME COLUMN requeued_count TO selected_count;
        END IF;
        IF EXISTS (SELECT 1 FROM information_schema.columns AS column_info WHERE column_info.table_name = 'search_reconciliation_runs' AND column_info.column_name = 'recovered_count') THEN
            ALTER TABLE search_reconciliation_runs RENAME COLUMN recovered_count TO updated_count;
        END IF;

        -- The old table was a queue-run ledger. Keep only durable document
        -- repair history; worker, lease, canary, and queue columns do not
        -- describe the synchronous search contract.
        ALTER TABLE search_reconciliation_runs
            ADD COLUMN IF NOT EXISTS selected_count BIGINT NOT NULL DEFAULT 0,
            ADD COLUMN IF NOT EXISTS embedded_count BIGINT NOT NULL DEFAULT 0,
            ADD COLUMN IF NOT EXISTS updated_count BIGINT NOT NULL DEFAULT 0,
            ADD COLUMN IF NOT EXISTS drifted_count BIGINT NOT NULL DEFAULT 0;
        ALTER TABLE search_reconciliation_runs
            DROP CONSTRAINT IF EXISTS embedding_reconciliation_runs_status_check,
            DROP CONSTRAINT IF EXISTS embedding_reconciliation_runs_outcome_check,
            DROP CONSTRAINT IF EXISTS embedding_reconciliation_runs_failure_contract_check,
            DROP CONSTRAINT IF EXISTS embedding_reconciliation_runs_count_check,
            DROP COLUMN IF EXISTS candidate_cutoff,
            DROP COLUMN IF EXISTS worker_id,
            DROP COLUMN IF EXISTS lease_token,
            DROP COLUMN IF EXISTS lease_until,
            DROP COLUMN IF EXISTS canary_job_id,
            DROP COLUMN IF EXISTS canary_attempted_at,
            DROP COLUMN IF EXISTS canary_outcome,
            DROP COLUMN IF EXISTS canary_failure_class,
            DROP COLUMN IF EXISTS canary_failure_code;
        -- These worker-era states have no synchronous equivalent; preserve
        -- their history as terminal maintenance failures before adding the
        -- document-centric status constraint.
        UPDATE search_reconciliation_runs
        SET status = 'failed',
            last_error = CASE
                WHEN btrim(COALESCE(last_error, '')) = ''
                THEN 'legacy reconciliation status retired during v2.6.1 cutover'
                ELSE last_error
            END,
            completed_at = COALESCE(completed_at, clock_timestamp()),
            updated_at = clock_timestamp()
        WHERE status IN ('reserved', 'deferred', 'ambiguous');
        DO $dense_mem_search_reconciliation_constraints$
        DECLARE
            unique_constraint TEXT;
        BEGIN
            FOR unique_constraint IN
                SELECT constraint_row.conname
                FROM pg_constraint AS constraint_row
                WHERE constraint_row.conrelid = 'search_reconciliation_runs'::regclass
                  AND constraint_row.contype = 'u'
                  AND pg_get_constraintdef(constraint_row.oid) LIKE '%embedding_contract_id%'
                  AND pg_get_constraintdef(constraint_row.oid) LIKE '%local_run_date%'
            LOOP
                EXECUTE format('ALTER TABLE search_reconciliation_runs DROP CONSTRAINT %I', unique_constraint);
            END LOOP;
        END;
        $dense_mem_search_reconciliation_constraints$;
        ALTER TABLE search_reconciliation_runs
            ADD CONSTRAINT search_reconciliation_runs_status_check
                CHECK (status IN ('running', 'completed', 'failed')),
            ADD CONSTRAINT search_reconciliation_runs_count_check
                CHECK (selected_count >= 0 AND embedded_count >= 0 AND updated_count >= 0 AND drifted_count >= 0);
        ALTER POLICY embedding_reconciliation_runs_system_access ON search_reconciliation_runs
            RENAME TO search_reconciliation_runs_system_access;
        CREATE INDEX IF NOT EXISTS search_reconciliation_runs_updated_idx
            ON search_reconciliation_runs(updated_at DESC, reconciliation_run_id DESC);
    END IF;
    IF to_regclass('search_projection_generations') IS NOT NULL
       AND EXISTS (SELECT 1 FROM information_schema.columns AS column_info WHERE column_info.table_name = 'search_projection_generations' AND column_info.column_name = 'failed_job_count')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns AS column_info WHERE column_info.table_name = 'search_projection_generations' AND column_info.column_name = 'drifted_count') THEN
        ALTER TABLE search_projection_generations RENAME COLUMN failed_job_count TO drifted_count;
    END IF;

    -- The source-binding policy still contains a subquery against the retired
    -- intent table. PostgreSQL treats that policy expression as a dependency,
    -- so remove it explicitly before the table retirement loop.
    DROP POLICY IF EXISTS evidence_fragments_remember_source_bind ON evidence_fragments;

    -- Assessment history is now keyed by semantic_assessments. These
    -- constraints are added only after the old placement foreign keys and
    -- columns have been removed.
    IF to_regclass('entity_resolution_events') IS NOT NULL THEN
        ALTER TABLE entity_resolution_events
            DROP CONSTRAINT IF EXISTS entity_resolution_events_assessment_ref,
            ADD CONSTRAINT entity_resolution_events_assessment_ref
                FOREIGN KEY (team_id, assessment_id)
                REFERENCES semantic_assessments(team_id, semantic_assessment_id) ON DELETE RESTRICT;
    END IF;
    IF to_regclass('verification_events') IS NOT NULL THEN
        ALTER TABLE verification_events
            DROP CONSTRAINT IF EXISTS verification_events_assessment_ref,
            ADD CONSTRAINT verification_events_assessment_ref
                FOREIGN KEY (team_id, assessment_id)
                REFERENCES semantic_assessments(team_id, semantic_assessment_id) ON DELETE RESTRICT;
    END IF;
    IF to_regclass('review_tasks') IS NOT NULL THEN
        ALTER TABLE review_tasks
            DROP CONSTRAINT IF EXISTS review_tasks_assessment_ref,
            ADD CONSTRAINT review_tasks_assessment_ref
                FOREIGN KEY (team_id, assessment_id)
                REFERENCES semantic_assessments(team_id, semantic_assessment_id) ON DELETE RESTRICT;
    END IF;

    FOREACH retired_table IN ARRAY ARRAY[
        'placement_outcomes', 'placement_items', 'placement_assessments',
        'placement_runs', 'embedding_jobs', 'remember_source_revision_intents',
        'remember_supersession_intents', 'submission_assessment_response_revisions',
        'submission_quarantine_payloads', 'submission_quarantine_tombstones'
    ] LOOP
        IF to_regclass(retired_table) IS NOT NULL THEN
            EXECUTE format('DROP TABLE %I', retired_table);
        END IF;
    END LOOP;

    -- The retained marker policy allows writes only in audited system mode;
    -- the migration switches modes explicitly for this final authority write.
    PERFORM set_config('app.tx_mode', 'system', true);
    INSERT INTO v2_compatibility_markers (
        marker_kind, version, status, corpus_hash, gate_report_hash, metadata
    ) VALUES (
        'v2_cutover', 'dense-mem.v2.6.1.cutover.v1', 'compatible',
        'sha256:' || source_hash,
        'sha256:' || attempt_hash,
        jsonb_build_object(
            'release', 'dense-mem.v2.6.1',
            'source_count', source_count,
            'attempt_count', attempt_count,
            'source_hash', source_hash,
            'attempt_hash', attempt_hash,
            'placement_item_count', item_source_count,
            'placement_item_hash', item_source_hash,
            'placement_outcome_count', outcome_source_count,
            'placement_outcome_hash', outcome_source_hash,
            'assessment_history_count', assessment_source_count,
            'assessment_history_hash', assessment_source_hash,
            'source_revision_intent_count', source_intent_count,
            'source_revision_intent_hash', source_intent_hash,
            'supersession_intent_count', supersession_intent_count,
            'supersession_intent_hash', supersession_intent_hash,
            'quarantine_payload_count', quarantine_payload_count,
            'quarantine_payload_hash', quarantine_payload_hash,
            'quarantine_payload_event_count', quarantine_payload_event_count,
            'quarantine_payload_event_hash', quarantine_payload_event_hash,
            'quarantine_tombstone_count', quarantine_tombstone_count,
            'quarantine_tombstone_hash', quarantine_tombstone_hash,
            'correction_superseded_count', correction_superseded_count,
            'active_search_documents_verified', true,
            'retired_tables_dropped', true
        )
    )
    ON CONFLICT (marker_kind, version) DO NOTHING;
    PERFORM set_config('app.tx_mode', 'migration', true);
END;
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'v2.6.1 synchronous Remember migration is irreversible; restore a verified snapshot and boot the previous binary';
END;
$$;
-- +goose StatementEnd
