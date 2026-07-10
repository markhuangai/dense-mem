-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'system', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE memory_placement_runs
    ADD COLUMN IF NOT EXISTS pipeline_version TEXT NOT NULL DEFAULT 'legacy-v1',
	ADD COLUMN IF NOT EXISTS actor_profile_id UUID NULL,
	ADD COLUMN IF NOT EXISTS actor_role TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS proposal JSONB NOT NULL DEFAULT '{"entities":[],"relationships":[]}'::jsonb,
    ADD COLUMN IF NOT EXISTS review_tasks JSONB NOT NULL DEFAULT '[]'::jsonb,
	ADD COLUMN IF NOT EXISTS security JSONB NOT NULL DEFAULT '{"quarantined":false,"signals":[]}'::jsonb,
	ADD COLUMN IF NOT EXISTS migration_refs JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS requires_acknowledgement BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS acknowledged_at TIMESTAMPTZ NULL;

ALTER TABLE memory_placement_runs
    DROP CONSTRAINT IF EXISTS memory_placement_runs_status_check;
ALTER TABLE memory_placement_runs
    ADD CONSTRAINT memory_placement_runs_status_check
    CHECK (status IN ('queued', 'processing', 'awaiting_review', 'completed', 'failed')) NOT VALID;
ALTER TABLE memory_placement_runs
    VALIDATE CONSTRAINT memory_placement_runs_status_check;

ALTER TABLE memory_placement_items
    ADD COLUMN IF NOT EXISTS evidence_indexes JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS fragment_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS assertion_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS relationship_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS assertion_status TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS policy_family TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS verifier_verdict TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS verifier_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS review_task_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS proposed_relationship JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS reviewed_relationship JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS security_signals JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE memory_placement_items
    DROP CONSTRAINT IF EXISTS memory_placement_items_category_check;
ALTER TABLE memory_placement_items
    ADD CONSTRAINT memory_placement_items_category_check
    CHECK (category IN (
        'fragment_only',
        'candidate_claim',
        'validated_claim',
        'promoted_fact',
        'needs_more_evidence',
        'rejected_false',
        'accepted_promoted',
        'rejected_explained',
        'assertion_candidate',
        'assertion_validated',
        'assertion_fact',
        'assertion_needs_review',
        'assertion_quarantined',
        'assertion_rejected'
    )) NOT VALID;
ALTER TABLE memory_placement_items
    VALIDATE CONSTRAINT memory_placement_items_category_check;

CREATE TABLE IF NOT EXISTS assertion_transition_events (
    event_id UUID PRIMARY KEY,
    profile_id UUID NOT NULL,
    ingest_id UUID NULL REFERENCES memory_placement_runs(ingest_id),
    placement_item_id UUID NULL,
    assertion_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    from_tier TEXT NOT NULL DEFAULT '',
    to_tier TEXT NOT NULL DEFAULT '',
    from_status TEXT NOT NULL DEFAULT '',
    to_status TEXT NOT NULL DEFAULT '',
    reason_code TEXT NOT NULL,
    source TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT assertion_transition_events_event_type_check
        CHECK (event_type IN (
			'proposed', 'retained_candidate', 'validated', 'promoted', 'rejected', 'quarantined',
            'review_requested', 'review_resolved', 'corrected', 'reversed',
            'superseded', 'acknowledged'
        )),
    CONSTRAINT assertion_transition_events_tier_check
        CHECK (from_tier IN ('', 'candidate', 'validated_claim', 'fact', 'dream')
           AND to_tier IN ('', 'candidate', 'validated_claim', 'fact', 'dream')),
    CONSTRAINT assertion_transition_events_status_check
        CHECK (from_status IN ('', 'active', 'needs_review', 'quarantined', 'superseded', 'disputed', 'retracted', 'rejected')
           AND to_status IN ('', 'active', 'needs_review', 'quarantined', 'superseded', 'disputed', 'retracted', 'rejected'))
);

CREATE INDEX IF NOT EXISTS idx_assertion_transition_profile_time
    ON assertion_transition_events(profile_id, occurred_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS idx_assertion_transition_profile_type_time
    ON assertion_transition_events(profile_id, event_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_assertion_transition_assertion_time
    ON assertion_transition_events(assertion_id, occurred_at ASC)
    WHERE assertion_id <> '';

ALTER TABLE assertion_transition_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE assertion_transition_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS assertion_transition_events_system_access ON assertion_transition_events;
CREATE POLICY assertion_transition_events_system_access ON assertion_transition_events
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

CREATE TABLE IF NOT EXISTS recall_memory_review_queue (
    review_id UUID PRIMARY KEY,
    profile_id UUID NOT NULL,
    recall_id TEXT NOT NULL REFERENCES recall_feedback_events(recall_id) ON DELETE CASCADE,
    knowledge_type TEXT NOT NULL,
    knowledge_id TEXT NOT NULL,
    reasons JSONB NOT NULL DEFAULT '[]'::jsonb,
    feedback_comment TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ NULL,
    CONSTRAINT recall_memory_review_queue_type_check
        CHECK (knowledge_type IN ('fragment', 'claim', 'fact', 'dream', 'assertion')),
    CONSTRAINT recall_memory_review_queue_status_check
        CHECK (status IN ('pending', 'resolved', 'dismissed')),
    CONSTRAINT recall_memory_review_queue_ref_unique
        UNIQUE (profile_id, recall_id, knowledge_type, knowledge_id)
);

CREATE INDEX IF NOT EXISTS idx_recall_memory_review_pending
    ON recall_memory_review_queue(profile_id, status, created_at ASC, review_id ASC);

ALTER TABLE recall_memory_review_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE recall_memory_review_queue FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS recall_memory_review_queue_system_access ON recall_memory_review_queue;
CREATE POLICY recall_memory_review_queue_system_access ON recall_memory_review_queue
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

CREATE OR REPLACE FUNCTION prevent_assertion_transition_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'assertion_transition_events is append-only: % operations are not allowed', TG_OP;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS assertion_transition_events_append_only ON assertion_transition_events;
CREATE TRIGGER assertion_transition_events_append_only
    BEFORE UPDATE OR DELETE ON assertion_transition_events
    FOR EACH ROW
    EXECUTE FUNCTION prevent_assertion_transition_mutation();

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

DROP TRIGGER IF EXISTS assertion_transition_events_append_only ON assertion_transition_events;
DROP FUNCTION IF EXISTS prevent_assertion_transition_mutation();
DROP TABLE IF EXISTS recall_memory_review_queue;
DROP TABLE IF EXISTS assertion_transition_events;

ALTER TABLE memory_placement_items
    DROP CONSTRAINT IF EXISTS memory_placement_items_category_check;
ALTER TABLE memory_placement_items
    ADD CONSTRAINT memory_placement_items_category_check
    CHECK (category IN (
        'fragment_only', 'candidate_claim', 'validated_claim', 'promoted_fact',
        'needs_more_evidence', 'rejected_false', 'accepted_promoted', 'rejected_explained'
    )) NOT VALID;
ALTER TABLE memory_placement_items
    VALIDATE CONSTRAINT memory_placement_items_category_check;
ALTER TABLE memory_placement_items
    DROP COLUMN IF EXISTS security_signals,
    DROP COLUMN IF EXISTS reviewed_relationship,
    DROP COLUMN IF EXISTS proposed_relationship,
    DROP COLUMN IF EXISTS review_task_id,
    DROP COLUMN IF EXISTS verifier_confidence,
    DROP COLUMN IF EXISTS verifier_verdict,
    DROP COLUMN IF EXISTS policy_family,
    DROP COLUMN IF EXISTS assertion_status,
    DROP COLUMN IF EXISTS tier,
    DROP COLUMN IF EXISTS relationship_type,
    DROP COLUMN IF EXISTS assertion_id,
    DROP COLUMN IF EXISTS fragment_ids,
    DROP COLUMN IF EXISTS evidence_indexes;

ALTER TABLE memory_placement_runs
    DROP CONSTRAINT IF EXISTS memory_placement_runs_status_check;
ALTER TABLE memory_placement_runs
    ADD CONSTRAINT memory_placement_runs_status_check
    CHECK (status IN ('queued', 'processing', 'completed', 'failed')) NOT VALID;
ALTER TABLE memory_placement_runs
    VALIDATE CONSTRAINT memory_placement_runs_status_check;
ALTER TABLE memory_placement_runs
    DROP COLUMN IF EXISTS acknowledged_at,
    DROP COLUMN IF EXISTS requires_acknowledgement,
	DROP COLUMN IF EXISTS security,
	DROP COLUMN IF EXISTS migration_refs,
    DROP COLUMN IF EXISTS review_tasks,
    DROP COLUMN IF EXISTS proposal,
	DROP COLUMN IF EXISTS actor_role,
	DROP COLUMN IF EXISTS actor_profile_id,
    DROP COLUMN IF EXISTS pipeline_version;

-- +goose StatementEnd
