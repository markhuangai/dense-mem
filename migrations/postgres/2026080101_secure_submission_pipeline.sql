-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - This migration adds an isolated raw-submission boundary. Legacy queued,
--   guarded, and processing shells are retained as failed audit records after
--   their raw input is staged; they are never eligible for another placement.
-- - The handoff refuses to run when an otherwise unfinished legacy shell
--   already has semantic or search output. That condition needs an explicit
--   operator decision instead of risking duplicate canonical knowledge.
-- - Deploy with legacy placement workers stopped. Queued legacy items are
--   copied to submission staging and become claimable only by the new worker.
-- - RLS mirrors the existing team/profile split. The expiry cleanup uses the
--   explicit system transaction mode and deletes at most 100 quarantines per
--   transaction through SKIP LOCKED.
-- - The down migration is only safe before secure-submission writes exist.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE IF NOT EXISTS submission_runs (
    team_id UUID NOT NULL,
    submission_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    request_hash TEXT NOT NULL DEFAULT '',
    source_summary TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    available_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    lease_until TIMESTAMPTZ NULL,
    worker_id TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    canonical_ingest_id UUID NULL,
    replaces_quarantined_submission_id UUID NULL,
    quarantine_expires_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, submission_id),
    UNIQUE (team_id, submission_id, owner_profile_id),
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT submission_runs_status_check
        CHECK (status IN ('queued', 'processing', 'completed', 'rejected', 'quarantined', 'failed')),
    CONSTRAINT submission_runs_attempts_check
        CHECK (attempts >= 0 AND max_attempts >= 1 AND attempts <= max_attempts),
    CONSTRAINT submission_runs_terminal_check CHECK (
        (status IN ('completed', 'rejected', 'quarantined', 'failed') AND completed_at IS NOT NULL)
        OR (status IN ('queued', 'processing') AND completed_at IS NULL)
    ),
    CONSTRAINT submission_runs_quarantine_expiry_check CHECK (
        (status = 'quarantined' AND quarantine_expires_at IS NOT NULL)
        OR (status <> 'quarantined' AND quarantine_expires_at IS NULL)
    ),
    CONSTRAINT submission_runs_quarantine_retention_check CHECK (
        status <> 'quarantined'
        OR quarantine_expires_at = completed_at + interval '24 hours'
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS submission_runs_idempotency_unique
    ON submission_runs(team_id, owner_profile_id, idempotency_key)
    WHERE idempotency_key <> '';
CREATE INDEX IF NOT EXISTS submission_runs_claim_idx
    ON submission_runs(team_id, status, available_at, created_at, submission_id);
CREATE INDEX IF NOT EXISTS submission_runs_quarantine_expiry_idx
    ON submission_runs(status, quarantine_expires_at, submission_id)
    WHERE status = 'quarantined';

CREATE TABLE IF NOT EXISTS submission_staged_proposals (
    team_id UUID NOT NULL,
    submission_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    proposal JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id, submission_id),
    FOREIGN KEY (team_id, submission_id, owner_profile_id)
        REFERENCES submission_runs(team_id, submission_id, owner_profile_id) ON DELETE CASCADE,
    CONSTRAINT submission_staged_proposals_object_check CHECK (jsonb_typeof(proposal) = 'object')
);

CREATE TABLE IF NOT EXISTS submission_staged_evidence (
    team_id UUID NOT NULL,
    submission_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    evidence_index INTEGER NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    source_type TEXT NOT NULL DEFAULT 'conversation',
    source_ref TEXT NOT NULL DEFAULT '',
    source_group TEXT NOT NULL DEFAULT '',
    authority TEXT NOT NULL DEFAULT 'primary',
    source_key TEXT NOT NULL DEFAULT '',
    source_revision TEXT NOT NULL DEFAULT '',
    previous_source_revision TEXT NOT NULL DEFAULT '',
    supersedes_evidence_ids TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    idempotency_key TEXT NOT NULL DEFAULT '',
    labels TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id, submission_id, evidence_index),
    FOREIGN KEY (team_id, submission_id, owner_profile_id)
        REFERENCES submission_runs(team_id, submission_id, owner_profile_id) ON DELETE CASCADE,
    CONSTRAINT submission_staged_evidence_index_check CHECK (evidence_index >= 0),
    CONSTRAINT submission_staged_evidence_content_check CHECK (btrim(content) <> ''),
    CONSTRAINT submission_staged_evidence_hash_check CHECK (btrim(content_hash) <> ''),
    CONSTRAINT submission_staged_evidence_source_type_check
        CHECK (source_type IN ('conversation', 'document', 'observation', 'manual')),
    CONSTRAINT submission_staged_evidence_authority_check
        CHECK (authority IN ('authoritative', 'primary', 'secondary', 'inferred', 'unknown')),
    CONSTRAINT submission_staged_evidence_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS submission_staged_evidence_submission_idx
    ON submission_staged_evidence(team_id, submission_id, evidence_index);

CREATE TABLE IF NOT EXISTS submission_assessments (
    team_id UUID NOT NULL,
    assessment_id UUID NOT NULL DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    request_id TEXT NOT NULL,
    model TEXT NOT NULL,
    tokenizer TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    candidate_context_tokens INTEGER NOT NULL,
    candidate_context_truncated BOOLEAN NOT NULL DEFAULT false,
    normalized_response JSONB NOT NULL,
    response_hash TEXT NOT NULL,
    validated_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id, assessment_id),
    UNIQUE (team_id, submission_id),
    FOREIGN KEY (team_id, submission_id, owner_profile_id)
        REFERENCES submission_runs(team_id, submission_id, owner_profile_id) ON DELETE CASCADE,
    CONSTRAINT submission_assessments_request_check CHECK (btrim(request_id) <> ''),
    CONSTRAINT submission_assessments_model_check CHECK (btrim(model) <> ''),
    CONSTRAINT submission_assessments_tokenizer_check CHECK (btrim(tokenizer) <> ''),
    CONSTRAINT submission_assessments_tokens_check CHECK (
        input_tokens >= 0 AND output_tokens >= 0 AND candidate_context_tokens >= 0
    ),
    CONSTRAINT submission_assessments_response_check CHECK (jsonb_typeof(normalized_response) = 'object'),
    CONSTRAINT submission_assessments_hash_check CHECK (btrim(response_hash) <> '')
);

CREATE TABLE IF NOT EXISTS submission_outcomes (
    team_id UUID NOT NULL,
    outcome_id UUID NOT NULL DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    outcome_kind TEXT NOT NULL,
    evidence_index INTEGER NULL,
    proposal_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    reason_code TEXT NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (team_id, outcome_id),
    FOREIGN KEY (team_id, submission_id, owner_profile_id)
        REFERENCES submission_runs(team_id, submission_id, owner_profile_id) ON DELETE CASCADE,
    CONSTRAINT submission_outcomes_kind_check CHECK (btrim(outcome_kind) <> ''),
    CONSTRAINT submission_outcomes_status_check CHECK (btrim(status) <> ''),
    CONSTRAINT submission_outcomes_evidence_index_check CHECK (evidence_index IS NULL OR evidence_index >= 0),
    CONSTRAINT submission_outcomes_details_check CHECK (jsonb_typeof(details) = 'object')
);

CREATE INDEX IF NOT EXISTS submission_outcomes_submission_idx
    ON submission_outcomes(team_id, submission_id, created_at, outcome_id);

ALTER TABLE submission_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE submission_staged_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_staged_proposals FORCE ROW LEVEL SECURITY;
ALTER TABLE submission_staged_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_staged_evidence FORCE ROW LEVEL SECURITY;
ALTER TABLE submission_assessments ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_assessments FORCE ROW LEVEL SECURITY;
ALTER TABLE submission_outcomes ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_outcomes FORCE ROW LEVEL SECURITY;

-- First-party workflows submit alongside normal clients, so they link to the
-- staged submission rather than fabricating a canonical ingest ID. Quarantine
-- cleanup clears those optional audit links before removing raw evidence.
ALTER TABLE hypotheses
    ADD COLUMN IF NOT EXISTS submitted_submission_id UUID NULL;
ALTER TABLE hypotheses
    DROP CONSTRAINT IF EXISTS hypotheses_submitted_submission_fk;
ALTER TABLE hypotheses
    ADD CONSTRAINT hypotheses_submitted_submission_fk
    FOREIGN KEY (team_id, submitted_submission_id)
    REFERENCES submission_runs(team_id, submission_id)
    ON DELETE SET NULL (submitted_submission_id);

ALTER TABLE hypothesis_feedback_events
    ADD COLUMN IF NOT EXISTS submitted_submission_id UUID NULL;
ALTER TABLE hypothesis_feedback_events
    DROP CONSTRAINT IF EXISTS hypothesis_feedback_events_submitted_submission_fk;
ALTER TABLE hypothesis_feedback_events
    ADD CONSTRAINT hypothesis_feedback_events_submitted_submission_fk
    FOREIGN KEY (team_id, submitted_submission_id)
    REFERENCES submission_runs(team_id, submission_id)
    ON DELETE SET NULL (submitted_submission_id);

ALTER TABLE skill_pack_imports
    ADD COLUMN IF NOT EXISTS submission_id UUID NULL;
ALTER TABLE skill_pack_imports
    DROP CONSTRAINT IF EXISTS skill_pack_imports_submission_fk;
ALTER TABLE skill_pack_imports
    ADD CONSTRAINT skill_pack_imports_submission_fk
    FOREIGN KEY (team_id, submission_id)
    REFERENCES submission_runs(team_id, submission_id)
    ON DELETE SET NULL (submission_id);
ALTER TABLE skill_pack_imports
    DROP CONSTRAINT IF EXISTS skill_pack_imports_status_check;
ALTER TABLE skill_pack_imports
    ADD CONSTRAINT skill_pack_imports_status_check
    CHECK (status IN ('inspecting', 'needs_review', 'submitted', 'applied', 'failed', 'rolled_back')) NOT VALID;
ALTER TABLE skill_pack_imports
    VALIDATE CONSTRAINT skill_pack_imports_status_check;

ALTER TABLE skill_pack_import_changes
    DROP CONSTRAINT IF EXISTS skill_pack_import_changes_entity_type_check;
ALTER TABLE skill_pack_import_changes
    ADD CONSTRAINT skill_pack_import_changes_entity_type_check
    CHECK (entity_type IN (
        'fragment',
        'claim',
        'fact',
        'relationship',
        'v2_ingest',
        'v2_placement_item',
        'submission'
    )) NOT VALID;
ALTER TABLE skill_pack_import_changes
    VALIDATE CONSTRAINT skill_pack_import_changes_entity_type_check;

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'submission_runs',
        'submission_staged_proposals',
        'submission_staged_evidence',
        'submission_assessments',
        'submission_outcomes'
    ]
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_select', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_insert', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_update', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_delete', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                )
            )', table_name || '_select', table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )', table_name || '_insert', table_name
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
            )', table_name || '_update', table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR DELETE USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (
                    current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = nullif(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = nullif(current_setting(''app.current_profile_id'', true), '''')::uuid
                )
            )', table_name || '_delete', table_name
        );
    END LOOP;
END $$;

-- Carry forward only unprocessed legacy input. The old append-only fragments
-- remain as failed audit records, while the copied raw input becomes the only
-- claimable work. The new worker scans staged legacy input before it can
-- produce any additional semantic state.
CREATE TEMP TABLE secure_submission_legacy_runs ON COMMIT DROP AS
SELECT run.team_id, run.placement_run_id, run.ingest_id, run.owner_profile_id
FROM placement_runs AS run
JOIN knowledge_ingests AS ingest
  ON ingest.team_id = run.team_id
 AND ingest.ingest_id = run.ingest_id
WHERE run.status IN ('queued', 'guarded', 'processing');

CREATE UNIQUE INDEX secure_submission_legacy_runs_pk
    ON secure_submission_legacy_runs(team_id, placement_run_id);

DO $$
DECLARE
    committed_count BIGINT;
BEGIN
    SELECT count(*) INTO committed_count
    FROM (
        SELECT observation.observation_id
        FROM secure_submission_legacy_runs AS legacy
        JOIN placement_items AS item
          ON item.team_id = legacy.team_id
         AND item.placement_run_id = legacy.placement_run_id
        JOIN relationship_observations AS observation
          ON observation.team_id = item.team_id
         AND observation.placement_item_id = item.placement_item_id

        UNION

        SELECT resolution.resolution_event_id
        FROM secure_submission_legacy_runs AS legacy
        JOIN placement_items AS item
          ON item.team_id = legacy.team_id
         AND item.placement_run_id = legacy.placement_run_id
        JOIN entity_resolution_events AS resolution
          ON resolution.team_id = item.team_id
         AND resolution.placement_item_id = item.placement_item_id

        UNION

        SELECT support.support_id
        FROM secure_submission_legacy_runs AS legacy
        JOIN evidence_fragments AS fragment
          ON fragment.team_id = legacy.team_id
         AND fragment.ingest_id = legacy.ingest_id
        JOIN relationship_evidence_supports AS support
          ON support.team_id = fragment.team_id
         AND support.fragment_id = fragment.fragment_id

        UNION

        SELECT document.search_document_id
        FROM secure_submission_legacy_runs AS legacy
        JOIN evidence_fragments AS fragment
          ON fragment.team_id = legacy.team_id
         AND fragment.ingest_id = legacy.ingest_id
        JOIN search_documents AS document
          ON document.team_id = fragment.team_id
         AND document.source_kind = 'evidence'
         AND document.source_id = fragment.fragment_id
    ) AS committed;

    IF committed_count > 0 THEN
        RAISE EXCEPTION
            'secure submission migration refused: % unfinished legacy placement records already have semantic or search output',
            committed_count;
    END IF;
END $$;

INSERT INTO submission_runs (
    team_id, submission_id, owner_profile_id, idempotency_key, request_hash,
    source_summary, status, attempts, max_attempts, available_at, created_at, updated_at
)
SELECT ingest.team_id, legacy.ingest_id, ingest.owner_profile_id,
       'legacy-upgrade:' || legacy.ingest_id::text,
       COALESCE(NULLIF(ingest.request_hash, ''), 'legacy-upgrade:' || legacy.ingest_id::text),
       ingest.source_summary, 'queued', 0, 5, clock_timestamp(), ingest.created_at, clock_timestamp()
FROM secure_submission_legacy_runs AS legacy
JOIN knowledge_ingests AS ingest
  ON ingest.team_id = legacy.team_id
 AND ingest.ingest_id = legacy.ingest_id
ON CONFLICT (team_id, submission_id) DO NOTHING;

INSERT INTO submission_staged_proposals (team_id, submission_id, owner_profile_id, proposal, created_at)
SELECT legacy.team_id, legacy.ingest_id, ingest.owner_profile_id, ingest.proposal, clock_timestamp()
FROM secure_submission_legacy_runs AS legacy
JOIN knowledge_ingests AS ingest
  ON ingest.team_id = legacy.team_id
 AND ingest.ingest_id = legacy.ingest_id
JOIN submission_runs AS submission
  ON submission.team_id = legacy.team_id AND submission.submission_id = legacy.ingest_id
ON CONFLICT (team_id, submission_id) DO NOTHING;

INSERT INTO submission_staged_evidence (
    team_id, submission_id, owner_profile_id, evidence_index, content, content_hash,
    source_type, source_ref, source_group, authority, source_key, source_revision,
    previous_source_revision, supersedes_evidence_ids, idempotency_key, labels, metadata, created_at
)
SELECT fragment.team_id, legacy.ingest_id, fragment.owner_profile_id, fragment.evidence_index,
       fragment.content, fragment.content_hash, fragment.source_type, fragment.source_ref,
       COALESCE(NULLIF(fragment.metadata->>'contract_source_group', ''), ''),
       CASE WHEN fragment.authority = 'derived' THEN 'inferred' ELSE fragment.authority END,
       CASE
           WHEN source.source_id IS NOT NULL AND revision.source_revision_id IS NOT NULL THEN source.source_key
           ELSE ''
       END,
       CASE
           WHEN source.source_id IS NOT NULL AND revision.source_revision_id IS NOT NULL THEN revision.revision_token
           ELSE ''
       END,
       CASE
           WHEN source.source_id IS NOT NULL AND revision.source_revision_id IS NOT NULL THEN revision.expected_previous_revision_token
           ELSE ''
       END,
       CASE
           WHEN jsonb_typeof(fragment.metadata->'supersedes_evidence_ids') = 'array'
               THEN ARRAY(SELECT jsonb_array_elements_text(fragment.metadata->'supersedes_evidence_ids'))
           ELSE ARRAY[]::TEXT[]
       END,
       COALESCE(fragment.metadata->>'evidence_idempotency_key', ''),
       fragment.labels, fragment.metadata, fragment.created_at
FROM secure_submission_legacy_runs AS legacy
JOIN evidence_fragments AS fragment
  ON fragment.team_id = legacy.team_id
 AND fragment.ingest_id = legacy.ingest_id
JOIN submission_runs AS submission
  ON submission.team_id = fragment.team_id AND submission.submission_id = legacy.ingest_id
LEFT JOIN evidence_sources AS source
  ON source.team_id = fragment.team_id AND source.source_id = fragment.source_id
LEFT JOIN evidence_source_revisions AS revision
  ON revision.team_id = fragment.team_id AND revision.source_revision_id = fragment.source_revision_id
ON CONFLICT (team_id, submission_id, evidence_index) DO NOTHING;

UPDATE placement_items AS item
SET status = 'failed', category = 'failed', error = 'secure_submission_upgrade_staged',
    updated_at = clock_timestamp()
FROM secure_submission_legacy_runs AS legacy
WHERE item.team_id = legacy.team_id
  AND item.placement_run_id = legacy.placement_run_id;

UPDATE placement_runs AS run
SET status = 'failed', worker_id = '', lease_until = NULL,
    completed_at = COALESCE(run.completed_at, clock_timestamp()),
    error = 'secure_submission_upgrade_staged', updated_at = clock_timestamp()
FROM secure_submission_legacy_runs AS legacy
WHERE run.team_id = legacy.team_id
  AND run.placement_run_id = legacy.placement_run_id;

UPDATE knowledge_ingests AS ingest
SET status = 'failed', completed_at = COALESCE(ingest.completed_at, clock_timestamp()),
    error = 'secure_submission_upgrade_staged', updated_at = clock_timestamp()
FROM secure_submission_legacy_runs AS legacy
WHERE ingest.team_id = legacy.team_id
  AND ingest.ingest_id = legacy.ingest_id;

-- Awaiting-review rows may already contain accepted semantic commits. Preserve
-- those records, cancel only open review tasks, and make unresolved items
-- terminally rejected with a bounded upgrade reason.
INSERT INTO placement_outcomes (
    team_id, placement_run_id, placement_item_id, owner_profile_id,
    outcome_kind, status, payload
)
SELECT item.team_id, item.placement_run_id, item.placement_item_id, item.owner_profile_id,
       'secure_submission_upgrade', 'rejected',
       jsonb_build_object('reason_code', 'legacy_review_canceled_by_secure_submission_upgrade')
FROM placement_items AS item
JOIN placement_runs AS run
  ON run.team_id = item.team_id AND run.placement_run_id = item.placement_run_id
WHERE run.status = 'awaiting_review'
  AND item.status = 'awaiting_review';

UPDATE review_tasks AS task
SET status = 'canceled', updated_at = clock_timestamp()
FROM placement_items AS item
JOIN placement_runs AS run
  ON run.team_id = item.team_id AND run.placement_run_id = item.placement_run_id
WHERE task.team_id = item.team_id
  AND task.placement_item_id = item.placement_item_id
  AND run.status = 'awaiting_review'
  AND task.status IN ('open', 'acknowledged');

UPDATE placement_items AS item
SET status = 'failed', category = 'failed',
    result = item.result || jsonb_build_object(
        'upgrade_reason', 'legacy_review_canceled_by_secure_submission_upgrade'
    ),
    updated_at = clock_timestamp()
FROM placement_runs AS run
WHERE run.team_id = item.team_id
  AND run.placement_run_id = item.placement_run_id
  AND run.status = 'awaiting_review'
  AND item.status = 'awaiting_review';

UPDATE placement_runs
SET status = 'completed', completed_at = COALESCE(completed_at, clock_timestamp()),
    error = 'legacy_review_canceled_by_secure_submission_upgrade',
    updated_at = clock_timestamp()
WHERE status = 'awaiting_review';

UPDATE knowledge_ingests AS ingest
SET status = 'completed', completed_at = COALESCE(ingest.completed_at, clock_timestamp()), updated_at = clock_timestamp()
FROM placement_runs AS run
WHERE run.team_id = ingest.team_id
  AND run.ingest_id = ingest.ingest_id
  AND run.status = 'completed'
  AND run.error = 'legacy_review_canceled_by_secure_submission_upgrade'
  AND ingest.status IN ('queued', 'guarded', 'processing');

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

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM submission_runs) THEN
        RAISE EXCEPTION 'cannot roll back 2026080101: secure submission records exist';
    END IF;
END $$;

DROP TABLE IF EXISTS submission_outcomes;
DROP TABLE IF EXISTS submission_assessments;
DROP TABLE IF EXISTS submission_staged_evidence;
DROP TABLE IF EXISTS submission_staged_proposals;

ALTER TABLE skill_pack_import_changes
    DROP CONSTRAINT IF EXISTS skill_pack_import_changes_entity_type_check;
ALTER TABLE skill_pack_import_changes
    ADD CONSTRAINT skill_pack_import_changes_entity_type_check
    CHECK (entity_type IN ('fragment', 'claim', 'fact', 'relationship', 'v2_ingest', 'v2_placement_item'));

ALTER TABLE skill_pack_imports
    DROP CONSTRAINT IF EXISTS skill_pack_imports_status_check;
ALTER TABLE skill_pack_imports
    ADD CONSTRAINT skill_pack_imports_status_check
    CHECK (status IN ('inspecting', 'needs_review', 'applied', 'failed', 'rolled_back'));
ALTER TABLE skill_pack_imports
    DROP CONSTRAINT IF EXISTS skill_pack_imports_submission_fk;
ALTER TABLE skill_pack_imports
    DROP COLUMN IF EXISTS submission_id;

ALTER TABLE hypothesis_feedback_events
    DROP CONSTRAINT IF EXISTS hypothesis_feedback_events_submitted_submission_fk;
ALTER TABLE hypothesis_feedback_events
    DROP COLUMN IF EXISTS submitted_submission_id;

ALTER TABLE hypotheses
    DROP CONSTRAINT IF EXISTS hypotheses_submitted_submission_fk;
ALTER TABLE hypotheses
    DROP COLUMN IF EXISTS submitted_submission_id;

DROP TABLE IF EXISTS submission_runs;

-- +goose StatementEnd
