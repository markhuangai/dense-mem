-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: replacing the three terminal-state checks takes short
-- ACCESS EXCLUSIVE locks; the migration aborts after the bounded 30-second
-- lock wait. NOT VALID installation keeps the existing rows from being
-- scanned until the explicit validation statements. The new tables are
-- additive and indexed by submission scope.
-- RLS impact: intent rows are readable by the owning team, writable only by
-- the owning profile, and updatable only for the one-time source activation.
-- An activated intent permits the owning profile to bind its staged fragment
-- once from a null source pair to that exact source revision. Result rows are
-- append-only and use the same team/profile boundary as the placement
-- assessment that produced them. Delete attempts retain their owner/system
-- visibility so append-only guards return explicit errors; those guards permit
-- deletion only for the system private-erasure transaction and its exact row
-- space.
-- Backfill: unfinished v2.5 Remember work is terminalized as failed with the
-- contract_superseded failure code. Accepted history is never rewritten. The
-- v2.6 worker only consumes new intent rows and persists v2.6 results.
-- Backward compatibility: none at runtime. Historical placement rows remain
-- intact, while only the v2.6 binary may resume intake after this coordinated
-- stopped-service cutover.
-- Rollback: down is allowed only before any v2.6 Remember ingest, intent, or
-- result row exists; after that workflow history is the irreversible boundary.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SELECT set_config('lock_timeout', '30s', true);

-- The historical v2.5 restart boundary normally drained this queue. Keep the
-- stopped-service cutover safe for rows that arrived after that drain or were
-- restored from a v2.5 snapshot: only Remember-originated, non-v2.6 work is
-- terminalized.
WITH affected_runs AS MATERIALIZED (
    SELECT run.team_id, run.placement_run_id, run.ingest_id
    FROM placement_runs AS run
    JOIN knowledge_ingests AS ingest
      ON ingest.team_id = run.team_id
     AND ingest.ingest_id = run.ingest_id
    WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
      AND COALESCE(ingest.metadata ->> 'contract_version', '') <> 'dense-mem.v2.6'
      AND (
          run.status IN ('queued', 'guarded', 'processing')
          OR EXISTS (
              SELECT 1
              FROM placement_items AS item
              WHERE item.team_id = run.team_id
                AND item.placement_run_id = run.placement_run_id
                AND item.status IN ('queued', 'processing')
          )
      )
), failed_ingests AS (
    UPDATE knowledge_ingests AS ingest
    SET status = 'failed',
        error = 'v2.6 Remember contract superseded; resubmit the complete submission',
        completed_at = COALESCE(ingest.completed_at, now()),
        updated_at = now()
    WHERE EXISTS (
        SELECT 1
        FROM affected_runs AS affected
        WHERE affected.team_id = ingest.team_id
          AND affected.ingest_id = ingest.ingest_id
    )
    RETURNING ingest.team_id, ingest.ingest_id
), failed_items AS (
    UPDATE placement_items AS item
    SET status = CASE
            WHEN item.status IN ('queued', 'processing') THEN 'failed'
            ELSE item.status
        END,
        category = CASE
            WHEN item.status IN ('queued', 'processing') THEN 'failed'
            ELSE item.category
        END,
        result = CASE
            WHEN item.status IN ('queued', 'processing') THEN jsonb_build_object(
                'contract_version', 'dense-mem.v2.6',
                'failure_stage', 'contract_superseded',
                'failure_code', 'contract_superseded',
                'retryable', true,
                'next_action', 'resubmit_submission',
                'reason', 'v2.5 Remember contract superseded'
            )
            ELSE item.result
        END,
        error = CASE
            WHEN item.status IN ('queued', 'processing')
                THEN 'v2.6 Remember contract superseded; resubmit the complete submission'
            ELSE item.error
        END,
        updated_at = now()
    WHERE EXISTS (
        SELECT 1
        FROM affected_runs AS affected
        WHERE affected.team_id = item.team_id
          AND affected.placement_run_id = item.placement_run_id
    )
    RETURNING item.team_id, item.placement_run_id
)
UPDATE placement_runs AS run
SET status = 'failed',
    error = 'v2.6 Remember contract superseded; resubmit the complete submission',
    worker_id = '',
    lease_until = NULL,
    assessor_attempt_id = NULL,
    assessor_attempted_at = NULL,
    completed_at = COALESCE(run.completed_at, now()),
    updated_at = now()
WHERE EXISTS (
    SELECT 1
    FROM affected_runs AS affected
    WHERE affected.team_id = run.team_id
      AND affected.placement_run_id = run.placement_run_id
);

ALTER TABLE knowledge_ingests
    DROP CONSTRAINT IF EXISTS knowledge_ingests_status_check;
ALTER TABLE knowledge_ingests
    ADD CONSTRAINT knowledge_ingests_status_check
    CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'rejected', 'failed')) NOT VALID;
ALTER TABLE knowledge_ingests
    VALIDATE CONSTRAINT knowledge_ingests_status_check;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_status_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_status_check
    CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'rejected', 'failed')) NOT VALID;
ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_status_check;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_completion_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_completion_check
    CHECK (
        (status IN ('completed', 'rejected', 'failed', 'quarantined') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed', 'rejected', 'failed', 'quarantined'))
    ) NOT VALID;
ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_completion_check;

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_status_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_status_check
    CHECK (status IN ('queued', 'processing', 'completed', 'rejected', 'failed', 'quarantined')) NOT VALID;
ALTER TABLE placement_items
    VALIDATE CONSTRAINT placement_items_status_check;

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_category_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_category_check
    CHECK (category IN ('pending', 'fragment_only', 'candidate', 'validated_claim', 'fact', 'quarantined', 'rejected', 'failed')) NOT VALID;
ALTER TABLE placement_items
    VALIDATE CONSTRAINT placement_items_category_check;

-- v2.6 Remember stores structural assessor support, not the removed
-- confidence/verdict/rationale contract. Keep the legacy audit columns for
-- Dream and correction history, but allow Remember rows to leave them empty.
ALTER TABLE verification_events
    ALTER COLUMN evidence_verdict DROP NOT NULL,
    ALTER COLUMN rationale DROP NOT NULL;

ALTER TABLE placement_assessments
    ADD COLUMN IF NOT EXISTS provider_turns INTEGER NOT NULL DEFAULT 1;
ALTER TABLE placement_assessments
    DROP CONSTRAINT IF EXISTS placement_assessments_provider_turns_check;
ALTER TABLE placement_assessments
    ADD CONSTRAINT placement_assessments_provider_turns_check
    CHECK (provider_turns BETWEEN 1 AND 5) NOT VALID;
ALTER TABLE placement_assessments
    VALIDATE CONSTRAINT placement_assessments_provider_turns_check;

CREATE TABLE IF NOT EXISTS remember_source_revision_intents (
    team_id UUID NOT NULL,
    intent_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    source_key TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    authority TEXT NOT NULL,
    revision_token TEXT NOT NULL,
    expected_previous_revision_token TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL,
    envelope JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_id UUID NULL,
    source_revision_id UUID NULL,
    space_id UUID NULL,
    space_generation BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, intent_id),
    CONSTRAINT remember_source_revision_intents_scope_unique
        UNIQUE (team_id, ingest_id, fragment_id, owner_profile_id),
    FOREIGN KEY (team_id, ingest_id, owner_profile_id)
        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_id, owner_profile_id)
        REFERENCES evidence_sources(team_id, source_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT remember_source_revision_intents_source_pair_check
        CHECK ((source_id IS NULL AND source_revision_id IS NULL) OR (source_id IS NOT NULL AND source_revision_id IS NOT NULL)),
    CONSTRAINT remember_source_revision_intents_source_key_check CHECK (btrim(source_key) <> ''),
    CONSTRAINT remember_source_revision_intents_kind_check CHECK (source_kind IN ('conversation', 'document', 'integration', 'manual')),
    CONSTRAINT remember_source_revision_intents_authority_check CHECK (authority IN ('authoritative', 'primary', 'secondary', 'inferred', 'unknown')),
    CONSTRAINT remember_source_revision_intents_revision_check CHECK (btrim(revision_token) <> ''),
    CONSTRAINT remember_source_revision_intents_hash_check CHECK (btrim(content_hash) <> ''),
    CONSTRAINT remember_source_revision_intents_envelope_check CHECK (jsonb_typeof(envelope) = 'object'),
    CONSTRAINT remember_source_revision_intents_space_pair_check CHECK (
        (space_id IS NULL AND space_generation IS NULL)
        OR (space_id IS NOT NULL AND space_generation > 0)
    )
);

CREATE INDEX IF NOT EXISTS remember_source_revision_intents_submission_idx
    ON remember_source_revision_intents(team_id, ingest_id, owner_profile_id, fragment_id);
CREATE INDEX IF NOT EXISTS remember_source_revision_intents_source_idx
    ON remember_source_revision_intents(team_id, source_key, revision_token);

CREATE TABLE IF NOT EXISTS remember_supersession_intents (
    team_id UUID NOT NULL,
    intent_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ingest_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    fragment_id UUID NOT NULL,
    target_fragment_id UUID NOT NULL,
    space_id UUID NULL,
    space_generation BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, intent_id),
    CONSTRAINT remember_supersession_intents_unique
        UNIQUE (team_id, ingest_id, fragment_id, target_fragment_id, owner_profile_id),
    FOREIGN KEY (team_id, ingest_id, owner_profile_id)
        REFERENCES knowledge_ingests(team_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, fragment_id, ingest_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, target_fragment_id)
        REFERENCES evidence_fragments(team_id, fragment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CONSTRAINT remember_supersession_intents_distinct_check CHECK (fragment_id <> target_fragment_id),
    CONSTRAINT remember_supersession_intents_space_pair_check CHECK (
        (space_id IS NULL AND space_generation IS NULL)
        OR (space_id IS NOT NULL AND space_generation > 0)
    )
);

CREATE INDEX IF NOT EXISTS remember_supersession_intents_submission_idx
    ON remember_supersession_intents(team_id, ingest_id, owner_profile_id, target_fragment_id);

CREATE TABLE IF NOT EXISTS submission_assessment_response_revisions (
    team_id UUID NOT NULL,
    revision_id UUID NOT NULL DEFAULT gen_random_uuid(),
    assessment_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    placement_run_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    revision_number INTEGER NOT NULL,
    provider_turns INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    candidate_context_tokens INTEGER NOT NULL,
    candidate_context_truncated BOOLEAN NOT NULL DEFAULT false,
    normalized_response JSONB NOT NULL,
    response_hash TEXT NOT NULL,
    validated_at TIMESTAMPTZ NOT NULL,
    space_id UUID NULL,
    space_generation BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, revision_id),
    CONSTRAINT submission_assessment_response_revisions_number_unique
        UNIQUE (team_id, assessment_id, revision_number),
    FOREIGN KEY (team_id, assessment_id)
        REFERENCES placement_assessments(team_id, assessment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id)
        REFERENCES placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CONSTRAINT submission_assessment_response_revisions_number_check CHECK (revision_number >= 1),
    CONSTRAINT submission_assessment_response_revisions_turn_check CHECK (provider_turns BETWEEN 1 AND 5),
    CONSTRAINT submission_assessment_response_revisions_token_check CHECK (
        input_tokens >= 0 AND output_tokens >= 0 AND candidate_context_tokens >= 0
    ),
    CONSTRAINT submission_assessment_response_revisions_response_check CHECK (jsonb_typeof(normalized_response) = 'object'),
    CONSTRAINT submission_assessment_response_revisions_hash_check CHECK (btrim(response_hash) <> ''),
    CONSTRAINT submission_assessment_response_revisions_space_pair_check CHECK (
        (space_id IS NULL AND space_generation IS NULL)
        OR (space_id IS NOT NULL AND space_generation > 0)
    )
);

CREATE INDEX IF NOT EXISTS submission_assessment_response_revisions_latest_idx
    ON submission_assessment_response_revisions(team_id, assessment_id, revision_number DESC);

CREATE OR REPLACE FUNCTION submission_relationship_result_shape_valid(
    result_disposition TEXT,
    result_reason TEXT,
    result_splits JSONB
)
RETURNS BOOLEAN AS $$
DECLARE
    split JSONB;
    split_count INTEGER;
    distinct_count INTEGER;
    minimum_index INTEGER;
    maximum_index INTEGER;
BEGIN
    IF jsonb_typeof(result_splits) IS DISTINCT FROM 'array' THEN
        RETURN false;
    END IF;
    split_count := jsonb_array_length(result_splits);
    IF result_disposition = 'stored' THEN
        IF result_reason <> '' OR split_count < 1 THEN
            RETURN false;
        END IF;
    ELSIF result_disposition = 'not_stored' THEN
        RETURN result_reason IN ('not_supported_by_evidence', 'stale_input', 'security_quarantine') AND split_count = 0;
    ELSE
        RETURN false;
    END IF;

    FOR split IN SELECT value FROM jsonb_array_elements(result_splits) LOOP
        IF jsonb_typeof(split) IS DISTINCT FROM 'object'
           OR (split - ARRAY['split_index', 'relationship_id', 'relationship_version', 'status']::TEXT[]) <> '{}'::jsonb
           OR NOT (split ?& ARRAY['split_index', 'relationship_id', 'relationship_version', 'status'])
           OR jsonb_typeof(split -> 'split_index') IS DISTINCT FROM 'number'
           OR (split ->> 'split_index') !~ '^(0|[1-9][0-9]*)$'
           OR jsonb_typeof(split -> 'relationship_id') IS DISTINCT FROM 'string'
           OR (split ->> 'relationship_id') !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
           OR jsonb_typeof(split -> 'relationship_version') IS DISTINCT FROM 'number'
           OR (split ->> 'relationship_version') !~ '^[1-9][0-9]*$'
           OR jsonb_typeof(split -> 'status') IS DISTINCT FROM 'string'
           OR split ->> 'status' <> 'active' THEN
            RETURN false;
        END IF;
    END LOOP;

    SELECT COUNT(DISTINCT (value ->> 'split_index')::INTEGER),
           MIN((value ->> 'split_index')::INTEGER),
           MAX((value ->> 'split_index')::INTEGER)
    INTO distinct_count, minimum_index, maximum_index
    FROM jsonb_array_elements(result_splits);
    RETURN distinct_count = split_count AND minimum_index = 0 AND maximum_index = split_count - 1;
END;
$$ LANGUAGE plpgsql IMMUTABLE;

CREATE TABLE IF NOT EXISTS submission_relationship_results (
    team_id UUID NOT NULL,
    result_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ingest_id UUID NOT NULL,
    placement_run_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    relationship_ref TEXT NOT NULL,
    disposition TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    splits JSONB NOT NULL DEFAULT '[]'::jsonb,
    space_id UUID NULL,
    space_generation BIGINT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, result_id),
    CONSTRAINT submission_relationship_results_ref_unique
        UNIQUE (team_id, placement_run_id, relationship_ref, owner_profile_id),
    FOREIGN KEY (team_id, ingest_id) REFERENCES knowledge_ingests(team_id, ingest_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id)
        REFERENCES placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES ownership_aliases(team_id, legacy_owner_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, space_id)
        REFERENCES memory_spaces(team_id, id) ON DELETE RESTRICT,
    CONSTRAINT submission_relationship_results_ref_check CHECK (btrim(relationship_ref) <> ''),
    CONSTRAINT submission_relationship_results_disposition_check CHECK (disposition IN ('stored', 'not_stored')),
    CONSTRAINT submission_relationship_results_shape_check
        CHECK (submission_relationship_result_shape_valid(disposition, reason, splits)),
    CONSTRAINT submission_relationship_results_space_pair_check CHECK (
        (space_id IS NULL AND space_generation IS NULL)
        OR (space_id IS NOT NULL AND space_generation > 0)
    )
);

CREATE INDEX IF NOT EXISTS submission_relationship_results_submission_idx
    ON submission_relationship_results(team_id, placement_run_id, owner_profile_id, relationship_ref);

DO $dense_mem_remember_reliability_rls$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'remember_source_revision_intents',
        'remember_supersession_intents',
        'submission_assessment_response_revisions',
        'submission_relationship_results'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_select', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_insert', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_update', table_name);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', table_name || '_delete', table_name);
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR SELECT USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (current_setting(''app.tx_mode'', true) IN (''team'', ''profile'')
                    AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND (current_setting(''app.tx_mode'', true) = ''team'' OR owner_profile_id = NULLIF(current_setting(''app.current_profile_id'', true), '''')::uuid))
            )',
            table_name || '_select', table_name
        );
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR INSERT WITH CHECK (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = NULLIF(current_setting(''app.current_profile_id'', true), '''')::uuid)
            )',
            table_name || '_insert', table_name
        );
        IF table_name IN ('remember_supersession_intents', 'submission_relationship_results') THEN
            EXECUTE format(
                'CREATE POLICY %I ON %I FOR UPDATE USING (
                    current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                    OR (current_setting(''app.tx_mode'', true) = ''profile''
                        AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                        AND owner_profile_id = NULLIF(current_setting(''app.current_profile_id'', true), '''')::uuid)
                ) WITH CHECK (
                    current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                    OR (current_setting(''app.tx_mode'', true) = ''profile''
                        AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                        AND owner_profile_id = NULLIF(current_setting(''app.current_profile_id'', true), '''')::uuid)
                )',
                table_name || '_update', table_name
            );
        ELSIF table_name = 'remember_source_revision_intents' THEN
            EXECUTE format(
                'CREATE POLICY %I ON %I FOR UPDATE USING (
                    current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                    OR (current_setting(''app.tx_mode'', true) = ''profile''
                        AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                        AND owner_profile_id = NULLIF(current_setting(''app.current_profile_id'', true), '''')::uuid)
                ) WITH CHECK (
                    current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                    OR (current_setting(''app.tx_mode'', true) = ''profile''
                        AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                        AND owner_profile_id = NULLIF(current_setting(''app.current_profile_id'', true), '''')::uuid)
                )',
                table_name || '_update', table_name
            );
        END IF;
        EXECUTE format(
            'CREATE POLICY %I ON %I FOR DELETE USING (
                current_setting(''app.tx_mode'', true) IN (''system'', ''migration'')
                OR (current_setting(''app.tx_mode'', true) = ''profile''
                    AND team_id = NULLIF(current_setting(''app.current_team_id'', true), '''')::uuid
                    AND owner_profile_id = NULLIF(current_setting(''app.current_profile_id'', true), '''')::uuid)
            )',
            table_name || '_delete', table_name
        );
    END LOOP;
END;
$dense_mem_remember_reliability_rls$;

DROP POLICY IF EXISTS evidence_fragments_remember_source_bind ON evidence_fragments;
CREATE POLICY evidence_fragments_remember_source_bind ON evidence_fragments
    FOR UPDATE
    USING (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid
        AND source_id IS NULL
        AND source_revision_id IS NULL
        AND EXISTS (
            SELECT 1
            FROM remember_source_revision_intents AS intent
            WHERE intent.team_id = evidence_fragments.team_id
              AND intent.ingest_id = evidence_fragments.ingest_id
              AND intent.owner_profile_id = evidence_fragments.owner_profile_id
              AND intent.fragment_id = evidence_fragments.fragment_id
              AND intent.source_id IS NOT NULL
              AND intent.source_revision_id IS NOT NULL
        )
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = NULLIF(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = NULLIF(current_setting('app.current_profile_id', true), '')::uuid
        AND source_id IS NOT NULL
        AND source_revision_id IS NOT NULL
        AND EXISTS (
            SELECT 1
            FROM remember_source_revision_intents AS intent
            WHERE intent.team_id = evidence_fragments.team_id
              AND intent.ingest_id = evidence_fragments.ingest_id
              AND intent.owner_profile_id = evidence_fragments.owner_profile_id
              AND intent.fragment_id = evidence_fragments.fragment_id
              AND intent.source_id = evidence_fragments.source_id
              AND intent.source_revision_id = evidence_fragments.source_revision_id
        )
    );

CREATE OR REPLACE FUNCTION prevent_evidence_fragment_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = OLD.space_id THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE'
       AND current_setting('app.tx_mode', true) = 'profile'
       AND OLD.source_id IS NULL
       AND OLD.source_revision_id IS NULL
       AND NEW.source_id IS NOT NULL
       AND NEW.source_revision_id IS NOT NULL
       AND (to_jsonb(NEW) - ARRAY['source_id', 'source_revision_id']::TEXT[])
           = (to_jsonb(OLD) - ARRAY['source_id', 'source_revision_id']::TEXT[])
       AND EXISTS (
           SELECT 1
           FROM remember_source_revision_intents AS intent
           WHERE intent.team_id = OLD.team_id
             AND intent.ingest_id = OLD.ingest_id
             AND intent.owner_profile_id = OLD.owner_profile_id
             AND intent.fragment_id = OLD.fragment_id
             AND intent.source_id = NEW.source_id
             AND intent.source_revision_id = NEW.source_revision_id
       ) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'evidence_fragments is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS evidence_fragments_append_only ON evidence_fragments;
CREATE TRIGGER evidence_fragments_append_only
    BEFORE UPDATE OR DELETE ON evidence_fragments
    FOR EACH ROW EXECUTE FUNCTION prevent_evidence_fragment_mutation();

CREATE OR REPLACE FUNCTION prevent_remember_source_intent_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF current_setting('app.tx_mode', true) = 'system'
           AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = OLD.space_id THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'remember source revision intents are activation-only';
    END IF;
    IF OLD.source_id IS NOT NULL OR OLD.source_revision_id IS NOT NULL
       OR NEW.team_id IS DISTINCT FROM OLD.team_id
       OR NEW.intent_id IS DISTINCT FROM OLD.intent_id
       OR NEW.ingest_id IS DISTINCT FROM OLD.ingest_id
       OR NEW.owner_profile_id IS DISTINCT FROM OLD.owner_profile_id
       OR NEW.fragment_id IS DISTINCT FROM OLD.fragment_id
       OR NEW.source_key IS DISTINCT FROM OLD.source_key
       OR NEW.source_kind IS DISTINCT FROM OLD.source_kind
       OR NEW.authority IS DISTINCT FROM OLD.authority
       OR NEW.revision_token IS DISTINCT FROM OLD.revision_token
       OR NEW.expected_previous_revision_token IS DISTINCT FROM OLD.expected_previous_revision_token
       OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
       OR NEW.envelope IS DISTINCT FROM OLD.envelope
       OR NEW.space_id IS DISTINCT FROM OLD.space_id
       OR NEW.space_generation IS DISTINCT FROM OLD.space_generation
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR (NEW.source_id IS NULL) <> (NEW.source_revision_id IS NULL) THEN
        RAISE EXCEPTION 'remember source revision intents are activation-only';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS remember_source_revision_intents_activation_guard ON remember_source_revision_intents;
CREATE TRIGGER remember_source_revision_intents_activation_guard
    BEFORE UPDATE OR DELETE ON remember_source_revision_intents
    FOR EACH ROW EXECUTE FUNCTION prevent_remember_source_intent_mutation();

DROP TRIGGER IF EXISTS remember_supersession_intents_append_only ON remember_supersession_intents;
CREATE TRIGGER remember_supersession_intents_append_only
    BEFORE UPDATE OR DELETE ON remember_supersession_intents
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

DROP TRIGGER IF EXISTS submission_assessment_response_revisions_append_only ON submission_assessment_response_revisions;
CREATE TRIGGER submission_assessment_response_revisions_append_only
    BEFORE UPDATE OR DELETE ON submission_assessment_response_revisions
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

DROP TRIGGER IF EXISTS submission_relationship_results_append_only ON submission_relationship_results;
CREATE TRIGGER submission_relationship_results_append_only
    BEFORE UPDATE OR DELETE ON submission_relationship_results
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('lock_timeout', '30s', true);

DO $dense_mem_remember_reliability_down$
BEGIN
    IF EXISTS (SELECT 1 FROM remember_source_revision_intents)
       OR EXISTS (SELECT 1 FROM remember_supersession_intents)
       OR EXISTS (SELECT 1 FROM submission_assessment_response_revisions)
       OR EXISTS (SELECT 1 FROM submission_relationship_results) THEN
        RAISE EXCEPTION 'cannot roll back 2026082301: v2.6 Remember intent or result history exists';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM knowledge_ingests
        WHERE error = 'v2.6 Remember contract superseded; resubmit the complete submission'
    ) THEN
        RAISE EXCEPTION 'cannot roll back 2026082301: v2.6 Remember terminal history exists';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM knowledge_ingests AS ingest
        WHERE ingest.metadata ->> '_dense_mem_telemetry_origin' = 'remember'
          AND ingest.metadata ->> 'contract_version' = 'dense-mem.v2.6'
    ) THEN
        RAISE EXCEPTION 'cannot roll back 2026082301: v2.6 Remember ingest history exists';
    END IF;
END;
$dense_mem_remember_reliability_down$;

DROP POLICY IF EXISTS evidence_fragments_remember_source_bind ON evidence_fragments;
DROP TRIGGER IF EXISTS evidence_fragments_append_only ON evidence_fragments;
CREATE TRIGGER evidence_fragments_append_only
    BEFORE UPDATE OR DELETE ON evidence_fragments
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();
DROP FUNCTION IF EXISTS prevent_evidence_fragment_mutation();

DROP TRIGGER IF EXISTS submission_relationship_results_append_only ON submission_relationship_results;
DROP TRIGGER IF EXISTS submission_assessment_response_revisions_append_only ON submission_assessment_response_revisions;
DROP TRIGGER IF EXISTS remember_supersession_intents_append_only ON remember_supersession_intents;
DROP TRIGGER IF EXISTS remember_source_revision_intents_activation_guard ON remember_source_revision_intents;
DROP FUNCTION IF EXISTS prevent_remember_source_intent_mutation();
DROP TABLE IF EXISTS submission_relationship_results;
DROP FUNCTION IF EXISTS submission_relationship_result_shape_valid(TEXT, TEXT, JSONB);
DROP TABLE IF EXISTS submission_assessment_response_revisions;
DROP TABLE IF EXISTS remember_supersession_intents;
DROP TABLE IF EXISTS remember_source_revision_intents;

ALTER TABLE verification_events
    ALTER COLUMN evidence_verdict SET NOT NULL,
    ALTER COLUMN rationale SET NOT NULL;

ALTER TABLE placement_assessments
    DROP CONSTRAINT IF EXISTS placement_assessments_provider_turns_check,
    DROP COLUMN IF EXISTS provider_turns;

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_category_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_category_check
    CHECK (category IN ('pending', 'fragment_only', 'candidate', 'validated_claim', 'fact', 'quarantined', 'failed'));
ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_status_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_status_check
    CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'quarantined'));
ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_completion_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_completion_check
    CHECK (
        (status IN ('completed', 'failed', 'quarantined') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed', 'failed', 'quarantined'))
    );
ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_status_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_status_check
    CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'failed'));
ALTER TABLE knowledge_ingests
    DROP CONSTRAINT IF EXISTS knowledge_ingests_status_check;
ALTER TABLE knowledge_ingests
    ADD CONSTRAINT knowledge_ingests_status_check
    CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'failed'));

-- +goose StatementEnd
