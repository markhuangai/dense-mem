-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - This is an exclusive V2.4 placement cutover migration. Adding the stable
--   claim key backfills placement metadata only; it does not alter semantic
--   records, search documents, vectors, or embedding jobs.
-- - The deployment must pause V2.3 placement workers before applying it. A
--   V2.3 worker cannot safely consume a V2.4 claim with a persisted assessor
--   decision.
-- - RLS policies mirror the existing owner-scoped placement/audit tables.
-- - Rollback is valid only before V2.4 writes; do not run an older worker
--   against persisted V2.4 assessments.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE teams
    ADD COLUMN IF NOT EXISTS config_version BIGINT NOT NULL DEFAULT 1;

ALTER TABLE teams
    DROP CONSTRAINT IF EXISTS teams_config_version_check;
ALTER TABLE teams
    ADD CONSTRAINT teams_config_version_check CHECK (config_version >= 1);

ALTER TABLE placement_items
    ADD COLUMN IF NOT EXISTS claim_key UUID NULL;

ALTER TABLE placement_items
    ADD COLUMN IF NOT EXISTS assessor_attempt_id UUID NULL,
    ADD COLUMN IF NOT EXISTS assessor_attempted_at TIMESTAMPTZ NULL;

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_assessor_attempt_pair_check;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_assessor_attempt_pair_check CHECK (
        (assessor_attempt_id IS NULL) = (assessor_attempted_at IS NULL)
    );

UPDATE placement_items
SET claim_key = gen_random_uuid()
WHERE claim_key IS NULL;

ALTER TABLE placement_items
    ALTER COLUMN claim_key SET NOT NULL;

ALTER TABLE placement_items
    ALTER COLUMN claim_key SET DEFAULT gen_random_uuid();

CREATE UNIQUE INDEX IF NOT EXISTS placement_items_team_claim_key_unique
    ON placement_items(team_id, claim_key);

ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_claim_ref_unique;
ALTER TABLE placement_items
    ADD CONSTRAINT placement_items_claim_ref_unique
    UNIQUE (team_id, placement_item_id, claim_key);

CREATE TABLE IF NOT EXISTS placement_assessments (
    team_id UUID NOT NULL,
    assessment_id UUID NOT NULL DEFAULT gen_random_uuid(),
    placement_item_id UUID NOT NULL,
    claim_key UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    request_id TEXT NOT NULL,
    assessor_contract_version TEXT NOT NULL,
    model TEXT NOT NULL,
    prompt_revision TEXT NOT NULL,
    tokenizer TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    candidate_context_tokens INTEGER NOT NULL,
    candidate_context_truncated BOOLEAN NOT NULL DEFAULT false,
    normalized_response JSONB NOT NULL,
    response_hash TEXT NOT NULL,
    validated_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, assessment_id),
    UNIQUE (team_id, placement_item_id),
    UNIQUE (team_id, claim_key),
    CONSTRAINT placement_assessments_item_owner_ref
    FOREIGN KEY (team_id, placement_item_id, owner_profile_id)
        REFERENCES placement_items(team_id, placement_item_id, owner_profile_id)
        ON DELETE RESTRICT,
	CONSTRAINT placement_assessments_claim_ref
	FOREIGN KEY (team_id, placement_item_id, claim_key)
		REFERENCES placement_items(team_id, placement_item_id, claim_key)
		ON DELETE RESTRICT,
    CONSTRAINT placement_assessments_request_nonempty CHECK (btrim(request_id) <> ''),
    CONSTRAINT placement_assessments_contract_nonempty CHECK (btrim(assessor_contract_version) <> ''),
    CONSTRAINT placement_assessments_model_nonempty CHECK (btrim(model) <> ''),
    CONSTRAINT placement_assessments_prompt_nonempty CHECK (btrim(prompt_revision) <> ''),
    CONSTRAINT placement_assessments_tokenizer_nonempty CHECK (btrim(tokenizer) <> ''),
    CONSTRAINT placement_assessments_token_counts_check CHECK (
        input_tokens >= 0
        AND output_tokens >= 0
        AND candidate_context_tokens >= 0
    ),
    CONSTRAINT placement_assessments_response_hash_nonempty CHECK (btrim(response_hash) <> ''),
    CONSTRAINT placement_assessments_response_object_check CHECK (jsonb_typeof(normalized_response) = 'object')
);

CREATE INDEX IF NOT EXISTS placement_assessments_owner_created_idx
    ON placement_assessments(team_id, owner_profile_id, created_at ASC);

DROP TRIGGER IF EXISTS placement_assessments_append_only ON placement_assessments;
CREATE TRIGGER placement_assessments_append_only
    BEFORE UPDATE OR DELETE ON placement_assessments
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE placement_assessments ENABLE ROW LEVEL SECURITY;
ALTER TABLE placement_assessments FORCE ROW LEVEL SECURITY;

CREATE POLICY placement_assessments_select ON placement_assessments
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

CREATE POLICY placement_assessments_insert ON placement_assessments
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'team'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );

ALTER TABLE entity_resolution_events
    ADD COLUMN IF NOT EXISTS assessment_id UUID NULL;
ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_assessment_ref;
ALTER TABLE entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_assessment_ref
    FOREIGN KEY (team_id, assessment_id)
    REFERENCES placement_assessments(team_id, assessment_id)
    ON DELETE RESTRICT;

ALTER TABLE verification_events
    ADD COLUMN IF NOT EXISTS assessment_id UUID NULL,
    ADD COLUMN IF NOT EXISTS assessment_policy_version TEXT NULL,
    ADD COLUMN IF NOT EXISTS threshold_used NUMERIC(12,10) NULL,
    ADD COLUMN IF NOT EXISTS gate_result TEXT NULL;

ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_assessment_ref;
ALTER TABLE verification_events
    ADD CONSTRAINT verification_events_assessment_ref
    FOREIGN KEY (team_id, assessment_id)
    REFERENCES placement_assessments(team_id, assessment_id)
    ON DELETE RESTRICT;

ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_threshold_used_check;
ALTER TABLE verification_events
    ADD CONSTRAINT verification_events_threshold_used_check
    CHECK (threshold_used IS NULL OR (threshold_used >= 0 AND threshold_used <= 1));

ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_gate_result_check;
ALTER TABLE verification_events
    ADD CONSTRAINT verification_events_gate_result_check
    CHECK (gate_result IS NULL OR gate_result IN (
        'meets_write_threshold', 'below_write_threshold', 'not_applicable'
    ));

ALTER TABLE review_tasks
    ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS assessment_id UUID NULL;

ALTER TABLE review_tasks
    DROP CONSTRAINT IF EXISTS review_tasks_version_check;
ALTER TABLE review_tasks
    ADD CONSTRAINT review_tasks_version_check CHECK (version >= 1);

ALTER TABLE review_tasks
    DROP CONSTRAINT IF EXISTS review_tasks_assessment_ref;
ALTER TABLE review_tasks
    ADD CONSTRAINT review_tasks_assessment_ref
    FOREIGN KEY (team_id, assessment_id)
    REFERENCES placement_assessments(team_id, assessment_id)
    ON DELETE RESTRICT;

ALTER TABLE review_tasks
    DROP CONSTRAINT IF EXISTS review_tasks_status_check;
ALTER TABLE review_tasks
    ADD CONSTRAINT review_tasks_status_check
    CHECK (status IN ('open', 'acknowledged', 'resolved', 'canceled', 'expired'));

CREATE INDEX IF NOT EXISTS review_tasks_open_expiry_idx
    ON review_tasks(team_id, expires_at ASC, review_task_id)
    WHERE status IN ('open', 'acknowledged')
      AND expires_at IS NOT NULL;

-- Only pre-existing semantic tasks acquire the V2.4 review deadline. Legacy
-- workflow tasks retain their prior timing and remain outside this transition.
UPDATE review_tasks
SET expires_at = statement_timestamp() + interval '7 days',
    updated_at = statement_timestamp()
WHERE status IN ('open', 'acknowledged')
  AND expires_at IS NULL
  AND (
      payload ? 'semantic_kind'
      OR (task_type = 'identity_needs_review' AND reason = 'ambiguous_entity')
      OR (task_type = 'predicate_needs_review' AND reason = 'unknown_predicate')
  );

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

DROP INDEX IF EXISTS review_tasks_open_expiry_idx;
ALTER TABLE review_tasks
    DROP CONSTRAINT IF EXISTS review_tasks_assessment_ref,
    DROP CONSTRAINT IF EXISTS review_tasks_version_check,
    DROP COLUMN IF EXISTS assessment_id,
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS version;
ALTER TABLE review_tasks
    DROP CONSTRAINT IF EXISTS review_tasks_status_check;
UPDATE review_tasks
SET status = 'canceled',
    updated_at = statement_timestamp()
WHERE status = 'expired';
ALTER TABLE review_tasks
    ADD CONSTRAINT review_tasks_status_check
    CHECK (status IN ('open', 'acknowledged', 'resolved', 'canceled'));

ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_assessment_ref,
    DROP CONSTRAINT IF EXISTS verification_events_threshold_used_check,
    DROP CONSTRAINT IF EXISTS verification_events_gate_result_check,
    DROP COLUMN IF EXISTS assessment_id,
    DROP COLUMN IF EXISTS assessment_policy_version,
    DROP COLUMN IF EXISTS threshold_used,
    DROP COLUMN IF EXISTS gate_result;

ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_assessment_ref,
    DROP COLUMN IF EXISTS assessment_id;

DROP TABLE IF EXISTS placement_assessments;
ALTER TABLE placement_items
    DROP CONSTRAINT IF EXISTS placement_items_claim_ref_unique;
DROP INDEX IF EXISTS placement_items_team_claim_key_unique;
ALTER TABLE placement_items
    ALTER COLUMN claim_key DROP DEFAULT,
    DROP CONSTRAINT IF EXISTS placement_items_assessor_attempt_pair_check,
    DROP COLUMN IF EXISTS assessor_attempted_at,
    DROP COLUMN IF EXISTS assessor_attempt_id,
    DROP COLUMN IF EXISTS claim_key;

ALTER TABLE teams DROP CONSTRAINT IF EXISTS teams_config_version_check;
ALTER TABLE teams DROP COLUMN IF EXISTS config_version;

-- +goose StatementEnd
