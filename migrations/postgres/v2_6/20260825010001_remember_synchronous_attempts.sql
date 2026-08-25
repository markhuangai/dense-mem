-- +goose Up
-- +goose StatementBegin

-- v2.6.1 request-scoped Remember terminal history. The legacy placement and
-- embedding tables remain readable during this additive migration; the
-- destructive retirement is a separate stopped-service release boundary.
-- Lock/rewrite impact: new tables and indexes are additive; the marker insert
-- takes a short system-table lock and aborts on lock timeout.
-- RLS impact: attempts, events, artifacts, and assessments are owner-scoped;
-- system and migration modes are the only administrative access paths.
-- Backfill: existing placement history is not rewritten by this additive
-- migration; the stopped-service retirement must copy and hash it first.
-- Backward compatibility: the v2.6.1 binary requires the new marker while
-- legacy placement rows remain readable for migration verification only.
-- Rollback: the Down section is intentionally irreversible after marker
-- creation; restore a verified snapshot and boot the previous binary.
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
    CONSTRAINT remember_attempts_outcome_check CHECK (outcome IN ('completed', 'rejected', 'quarantined', 'failed', 'replayed')),
    CONSTRAINT remember_attempts_result_check CHECK (jsonb_typeof(public_result) = 'object'),
    CONSTRAINT remember_attempts_counts_check CHECK (evidence_count >= 0 AND relationship_count >= 0 AND document_count >= 0 AND assessor_turns >= 0),
    CONSTRAINT remember_attempts_space_pair_check CHECK ((space_id IS NULL AND space_generation IS NULL) OR (space_id IS NOT NULL AND space_generation > 0)),
    CONSTRAINT remember_attempts_key_check CHECK (btrim(idempotency_key) <> '' AND btrim(request_hash) <> ''),
    CONSTRAINT remember_attempts_kind_check CHECK (submission_kind IN ('remember', 'relationship_correction'))
);

CREATE UNIQUE INDEX IF NOT EXISTS remember_attempts_canonical_key_idx
    ON remember_attempts(team_id, owner_profile_id, idempotency_key)
    WHERE outcome IN ('completed', 'rejected', 'quarantined');
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
    CONSTRAINT remember_attempt_events_metadata_check CHECK (jsonb_typeof(metadata) = 'object')
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
    CONSTRAINT remember_failure_artifacts_expiry_check CHECK (expires_at >= captured_at)
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
    response_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, semantic_assessment_id),
    UNIQUE (team_id, attempt_id),
    CONSTRAINT semantic_assessments_history_check CHECK (jsonb_typeof(response_history) = 'array'),
    CONSTRAINT semantic_assessments_revision_check CHECK (accepted_revision IS NULL OR accepted_revision >= 1),
    CONSTRAINT semantic_assessments_turn_check CHECK (provider_turns BETWEEN 0 AND 3)
);

ALTER TABLE remember_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_attempts FORCE ROW LEVEL SECURITY;
ALTER TABLE remember_attempt_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_attempt_events FORCE ROW LEVEL SECURITY;
ALTER TABLE remember_failure_artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE remember_failure_artifacts FORCE ROW LEVEL SECURITY;
ALTER TABLE semantic_assessments ENABLE ROW LEVEL SECURITY;
ALTER TABLE semantic_assessments FORCE ROW LEVEL SECURITY;

CREATE POLICY remember_attempts_scope ON remember_attempts
    FOR ALL
    USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid)
    );
CREATE POLICY remember_attempt_events_scope ON remember_attempt_events
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration') OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration') OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid));
CREATE POLICY remember_failure_artifacts_scope ON remember_failure_artifacts
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration') OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration') OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid));
CREATE POLICY semantic_assessments_scope ON semantic_assessments
    FOR ALL
    USING (current_setting('app.tx_mode', true) IN ('system', 'migration') OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid))
    WITH CHECK (current_setting('app.tx_mode', true) IN ('system', 'migration') OR (team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid));

-- The cutover is stopped-service and fail-closed. Do not write a marker while
-- legacy placement work could still mutate or while an active-contract search
-- document lacks its required current vector.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM placement_runs
        WHERE status IN ('queued', 'guarded', 'processing')
    ) OR EXISTS (
        SELECT 1 FROM placement_items
        WHERE status IN ('queued', 'processing')
    ) THEN
        RAISE EXCEPTION 'v2.6.1 cutover blocked: active placement work remains';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM search_documents AS document
        JOIN search_index_generations AS generation
          ON generation.embedding_contract_id = document.embedding_contract_id
         AND generation.embedding_dimensions = document.embedding_dimensions
         AND generation.activation_state = 'active'
        JOIN embedding_contracts AS contract
          ON contract.embedding_contract_id = document.embedding_contract_id
         AND contract.lifecycle_state = 'active'
         AND contract.distance_metric = 'cosine'
        WHERE document.search_state <> 'current'
           OR document.embedding IS NULL
           OR document.embedding_dimensions <> contract.dimensions
    ) THEN
        RAISE EXCEPTION 'v2.6.1 cutover blocked: active-contract search document lacks a current valid vector';
    END IF;
END;
$$;

-- Copy Remember-origin history into the terminal-attempt and chronological
-- event projections before the legacy placement tables are retired. The
-- source rows remain untouched until the final stopped-service release step.
WITH legacy AS MATERIALIZED (
    SELECT ingest.team_id, ingest.ingest_id, ingest.owner_profile_id,
           ingest.idempotency_key, ingest.request_hash, ingest.created_at,
           ingest.completed_at, ingest.status,
           COALESCE(run.status, ingest.status) AS run_status,
           COALESCE(run.attempts, 0) AS attempts,
           COALESCE(run.max_attempts, 0) AS max_attempts,
           COALESCE(run.correlation_id, '') AS correlation_id,
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
       NULL, NULL,
       COALESCE(NULLIF(normalized.idempotency_key, ''), 'legacy:' || normalized.ingest_id::text),
       COALESCE(NULLIF(normalized.request_hash, ''), 'legacy:' || normalized.ingest_id::text),
       'dense-mem.v2.6.1', 'remember', normalized.outcome,
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

INSERT INTO remember_attempt_events (
    team_id, event_id, attempt_id, owner_profile_id, sequence_no,
    phase, event_kind, outcome, metadata, created_at
)
SELECT attempt.team_id, gen_random_uuid(), attempt.attempt_id, attempt.owner_profile_id, 1,
       'legacy_cutover', 'legacy_terminalized', attempt.outcome,
       jsonb_build_object('source', 'placement_runs', 'contract_version', 'dense-mem.v2.6.1'),
       COALESCE(attempt.completed_at, attempt.created_at)
FROM remember_attempts AS attempt
WHERE attempt.contract_version = 'dense-mem.v2.6.1'
  AND NOT EXISTS (
      SELECT 1 FROM remember_attempt_events AS event
      WHERE event.team_id = attempt.team_id AND event.attempt_id = attempt.attempt_id
  );

-- Preserve immutable assessor response history under the new attempt key. A
-- malformed or missing legacy response is represented as an empty history;
-- the migration never invents provider bytes.
INSERT INTO semantic_assessments (
    team_id, semantic_assessment_id, attempt_id, owner_profile_id,
    response_history, accepted_revision, provider_turns, response_hash, created_at
)
SELECT assessment.team_id, gen_random_uuid(), assessment.ingest_id,
       assessment.owner_profile_id,
       jsonb_agg(jsonb_build_object(
           'assessment_id', assessment.assessment_id::text,
           'normalized_response', assessment.normalized_response,
           'response_hash', assessment.response_hash,
           'provider_turns', assessment.provider_turns,
           'validated_at', assessment.validated_at
       ) ORDER BY assessment.validated_at, assessment.assessment_id),
       1, max(assessment.provider_turns), max(assessment.response_hash), min(assessment.validated_at)
FROM (
    SELECT DISTINCT ON (legacy.team_id, legacy.ingest_id, legacy.owner_profile_id, placement.assessment_id)
           legacy.team_id, legacy.ingest_id, legacy.owner_profile_id,
           placement.assessment_id, placement.normalized_response,
           placement.response_hash, placement.provider_turns, placement.validated_at
    FROM knowledge_ingests AS legacy
    JOIN placement_runs AS run ON run.team_id = legacy.team_id AND run.ingest_id = legacy.ingest_id
    JOIN placement_items AS item ON item.team_id = run.team_id AND item.placement_run_id = run.placement_run_id
    JOIN placement_assessments AS placement ON placement.team_id = item.team_id AND placement.placement_item_id = item.placement_item_id
    WHERE legacy.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
    ORDER BY legacy.team_id, legacy.ingest_id, legacy.owner_profile_id, placement.assessment_id, placement.validated_at DESC
) AS assessment
GROUP BY assessment.team_id, assessment.ingest_id, assessment.owner_profile_id
ON CONFLICT (team_id, attempt_id) DO NOTHING;

INSERT INTO v2_compatibility_markers (marker_id, marker_kind, version, status, corpus_hash, gate_report_hash, metadata, created_at)
VALUES (
    gen_random_uuid(), 'v2_cutover', 'dense-mem.v2.6.1.cutover.v1', 'compatible', '', '',
    jsonb_build_object(
        'release', 'v2.6.1', 'remember_mode', 'synchronous', 'status_tool', 'removed',
        'evaluation_1k', 'waived_by_maintainer',
        'remember_attempt_count', (SELECT count(*) FROM remember_attempts),
        'remember_attempt_event_count', (SELECT count(*) FROM remember_attempt_events),
        'semantic_assessment_count', (SELECT count(*) FROM semantic_assessments)
    ), now()
)
ON CONFLICT (marker_kind, version) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'v2.6.1 synchronous Remember migration is irreversible; restore a verified snapshot and boot the previous binary';
END;
$$;
-- +goose StatementEnd
