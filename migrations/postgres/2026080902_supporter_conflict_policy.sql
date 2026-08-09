-- +goose Up
-- +goose StatementBegin

-- This migration changes the sole active conflict policy. It preserves
-- historical identifiers, but active workflow state must not continue under
-- the old group-count interpretation.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE relationship_conflict_events
    DROP CONSTRAINT IF EXISTS relationship_conflict_events_action_check;

ALTER TABLE relationship_conflict_events
    ADD CONSTRAINT relationship_conflict_events_action_check CHECK (action IN (
        'opened', 'position_added', 'member_added', 'evaluated', 'marked_overdue',
        'resolved', 'relationship_updated', 'dismissed', 'ai_assessment_reserved',
        'ai_assessed', 'resolution_pending', 'evidence_retracted',
        'derived_replacement_staged', 'derived_replacement_failed', 'policy_migrated'
    )) NOT VALID;

CREATE TEMP TABLE conflict_policy_migration_cases ON COMMIT DROP AS
SELECT team_id,
       conflict_id,
       version AS previous_case_version,
       policy_version AS previous_policy_version,
       review_due_at,
       CASE WHEN review_due_at <= clock_timestamp() THEN clock_timestamp() ELSE review_due_at END AS next_review_at
FROM relationship_conflict_cases
WHERE status IN ('open', 'overdue');

CREATE TEMP TABLE conflict_policy_migration_attempts ON COMMIT DROP AS
SELECT attempt.team_id,
       attempt.assessment_attempt_id,
       attempt.conflict_id,
       attempt.case_version,
       attempt.policy_version
FROM relationship_conflict_ai_assessment_attempts AS attempt
JOIN conflict_policy_migration_cases AS migration
  ON migration.team_id = attempt.team_id
 AND migration.conflict_id = attempt.conflict_id
WHERE attempt.status = 'reserved';

UPDATE relationship_conflict_ai_assessment_attempts AS attempt
SET status = 'superseded',
    failure_class = 'policy_replaced',
    completed_at = clock_timestamp()
FROM conflict_policy_migration_attempts AS migration
WHERE attempt.team_id = migration.team_id
  AND attempt.assessment_attempt_id = migration.assessment_attempt_id;

INSERT INTO relationship_conflict_ai_assessment_events (
    team_id, assessment_attempt_id, action, outcome, metadata
)
SELECT team_id,
       assessment_attempt_id,
       'superseded',
       'policy_replaced',
       jsonb_build_object(
           'previous_case_version', case_version,
           'previous_policy_version', policy_version,
           'failure_class', 'policy_replaced'
       )
FROM conflict_policy_migration_attempts;

UPDATE relationship_conflict_resolution_plans AS plan
SET status = 'superseded',
    failure_reason = 'policy_replaced'
FROM conflict_policy_migration_cases AS migration
WHERE plan.team_id = migration.team_id
  AND plan.conflict_id = migration.conflict_id
  AND plan.status = 'resolution_pending';

UPDATE relationship_conflict_review_runs
SET status = 'failed',
    last_error = 'policy_replaced',
    lease_until = NULL,
    completed_at = COALESCE(completed_at, clock_timestamp()),
    updated_at = clock_timestamp()
WHERE status IN ('reserved', 'running')
  AND policy_version <> 'cross_profile_supporter_majority_after_ttl';

UPDATE relationship_conflict_cases AS conflict
SET policy_version = 'cross_profile_supporter_majority_after_ttl',
    version = conflict.version + 1,
    next_review_at = migration.next_review_at,
    attempts = 0,
    lease_worker_id = '',
    lease_until = NULL,
    last_error = '',
    last_review_run_id = NULL,
    updated_at = clock_timestamp()
FROM conflict_policy_migration_cases AS migration
WHERE conflict.team_id = migration.team_id
  AND conflict.conflict_id = migration.conflict_id;

INSERT INTO relationship_conflict_events (
    team_id, conflict_id, action, outcome, actor_kind, policy_version,
    idempotency_key, metadata
)
SELECT team_id,
       conflict_id,
       'policy_migrated',
       'supporter_majority_after_ttl',
       'system',
       'cross_profile_supporter_majority_after_ttl',
       'case:' || conflict_id::text || ':policy_migrated:supporter_majority_after_ttl',
       jsonb_build_object(
           'previous_case_version', previous_case_version,
           'current_case_version', previous_case_version + 1,
           'previous_policy_version', previous_policy_version,
           'current_policy_version', 'cross_profile_supporter_majority_after_ttl',
           'review_due_at', review_due_at
       )
FROM conflict_policy_migration_cases
ON CONFLICT (team_id, idempotency_key) WHERE idempotency_key <> '' DO NOTHING;

INSERT INTO relationship_conflict_events (
    team_id, conflict_id, action, outcome, actor_kind, policy_version,
    idempotency_key, metadata
)
SELECT migration.team_id,
       migration.conflict_id,
       'ai_assessed',
       'superseded',
       'system',
       'cross_profile_supporter_majority_after_ttl',
       'case:' || migration.conflict_id::text || ':assessment:' || migration.assessment_attempt_id::text || ':policy_replaced',
       jsonb_build_object(
           'assessment_attempt_id', migration.assessment_attempt_id,
           'previous_case_version', migration.case_version,
           'failure_class', 'policy_replaced'
       )
FROM conflict_policy_migration_attempts AS migration
ON CONFLICT (team_id, idempotency_key) WHERE idempotency_key <> '' DO NOTHING;

-- The legacy counters remain during this expand/contract release so older
-- replicas can continue selecting and writing the compatibility columns.
-- Source-group keys remain on members for lineage and identity.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DO $$
BEGIN
    RAISE EXCEPTION '2026080902_supporter_conflict_policy is irreversible because active conflict workflow and audit policy identity changed';
END $$;

-- +goose StatementEnd
