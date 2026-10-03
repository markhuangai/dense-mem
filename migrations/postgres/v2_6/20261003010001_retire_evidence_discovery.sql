-- Lock/rewrite impact: row updates and audit inserts; no rewrite or new indexes. A
-- 30-second lock timeout bounds waits on unfinished operational rows.
-- RLS impact: transaction-local migration authority covers every team and
-- generation; request-selectable security context and RLS policies are unchanged.
-- Backfill: cancel queued/running evidence-discovery runs and abandon reserved
-- target attempts, preserving their counters, identities, and dispatch history.
-- Backward compatibility: stop all old application instances before migration.
-- Graph runs, completed runs, hypotheses, and append-only provenance remain intact.
-- Rollback: retirement is irreversible. Down preserves cancellations and
-- abandoned reservations; a binary rollback does not resurrect unfinished work.

-- +goose Up
SET LOCAL app.tx_mode = 'migration';
SET LOCAL app.current_team_id = '';
SET LOCAL app.current_profile_id = '';
SET LOCAL app.allowed_space_ids = '';
SET LOCAL lock_timeout = '30s';

WITH retired AS (
    UPDATE dream_cycle_runs
    SET status = 'cancelled',
        error = 'evidence discovery Dream generation is retired',
        outcome_summary = outcome_summary || '{"evidence_discovery_retired":1}'::jsonb,
        lease_until = NULL,
        completed_at = COALESCE(completed_at, now()),
        updated_at = now()
    WHERE lane = 'evidence_discovery' AND status IN ('queued', 'running')
    RETURNING *
)
INSERT INTO audit_log (
    team_id, operation, entity_type, entity_id, after_payload, actor_role, metadata
)
SELECT team_id, 'dream_cycle_completed', 'dream_cycle_run', run_id::text,
    jsonb_build_object(
        'status', status,
        'input_relationships', input_count,
        'created_dreams', created_hypotheses,
        'rejected_dreams', rejected_hypotheses,
        'attempted_paths', attempted_paths,
        'provider_proposals', provider_proposals,
        'lane', lane,
        'evidence_targets', evidence_targets,
        'evaluated_evidence_targets', evaluated_evidence_targets,
        'outcomes', outcome_summary
    ),
    'system', jsonb_build_object('scheduled', true, 'migration_version', '20261003010001')
FROM retired;

UPDATE dream_evidence_target_attempts
SET status = 'abandoned',
    abandoned_at = now(),
    reservation_expires_at = now(),
    updated_at = now()
WHERE status = 'reserved';

-- +goose Down
SELECT 1;
