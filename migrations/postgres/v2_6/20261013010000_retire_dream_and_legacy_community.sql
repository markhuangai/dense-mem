-- Lock/rewrite impact: indexed operational row updates and audit inserts; no table rewrite or new indexes. Lock waits are bounded to 30 seconds.
-- RLS impact: transaction-local migration authority retires all teams; system authority updates global configuration without changing request policies.
-- Backfill: cancel unfinished Dream and legacy community runs and abandon outstanding evidence reservations; retain completed and append-only history.
-- Backward compatibility: stop old application instances before applying this migration and start the matching v2.7.0 runtime afterward.
-- Rollback: retirement is irreversible; Down retains cancellations and disabled legacy settings and does not resurrect generators or delete history.

-- +goose Up
SET LOCAL app.tx_mode = 'migration';
SET LOCAL app.current_team_id = '';
SET LOCAL app.current_profile_id = '';
SET LOCAL app.allowed_space_ids = '';
SET LOCAL lock_timeout = '30s';

WITH retired AS (
    UPDATE dream_cycle_runs
    SET status = 'cancelled',
        error = 'Dream generation is retired',
        outcome_summary = outcome_summary || '{"dream_generation_retired":1}'::jsonb,
        lease_until = NULL,
        completed_at = COALESCE(completed_at, now()),
        updated_at = now()
    WHERE status IN ('queued', 'running')
    RETURNING *
)
INSERT INTO audit_log (
    team_id, operation, entity_type, entity_id, after_payload, actor_role, metadata
)
SELECT team_id, 'dream_cycle_completed', 'dream_cycle_run', run_id::text,
    jsonb_build_object('status', status, 'lane', lane,
        'created_dreams', created_hypotheses, 'outcomes', outcome_summary),
    'system', '{"migration_version":"20261013010000","retired":true}'::jsonb
FROM retired;

UPDATE dream_evidence_target_attempts
SET status = 'abandoned', abandoned_at = now(),
    reservation_expires_at = now(), updated_at = now()
WHERE status = 'reserved';

WITH retired AS (
    UPDATE community_snapshot_runs
    SET status = 'cancelled', error = 'Legacy community processing is retired',
        lease_until = NULL, completed_at = COALESCE(completed_at, now()), updated_at = now()
    WHERE status = 'running' AND algorithm_kind <> 'ontology_topic'
    RETURNING *
)
INSERT INTO audit_log (
    team_id, operation, entity_type, entity_id, after_payload, actor_role, metadata
)
SELECT team_id, 'community_run_completed', 'community_run', run_id::text,
    jsonb_build_object('status', status, 'algorithm_kind', algorithm_kind,
        'node_count', node_count, 'community_count', community_count),
    'system', '{"migration_version":"20261013010000","retired":true}'::jsonb
FROM retired;

SET LOCAL app.tx_mode = 'system';
INSERT INTO app_config (key, value)
VALUES ('ONTOLOGY_MAINTENANCE_ENABLED', 'false')
ON CONFLICT (key) DO NOTHING;

WITH retired AS (
    UPDATE app_config SET value = 'false', updated_at = now()
    WHERE key IN ('DREAMING_ENABLED', 'DREAMING_FORCE_ENABLED',
        'DREAMING_REFLECT_ENABLED', 'DREAMING_REEVALUATE_ENABLED',
        'DREAMING_DREAM_ENABLED', 'COMMUNITY_DETECTION_ENABLED')
        AND value IS DISTINCT FROM 'false'
    RETURNING key
)
INSERT INTO audit_log (operation, entity_type, entity_id, after_payload, actor_role, metadata)
SELECT 'APP_CONFIG_UPDATE', 'app_config', 'legacy_processing_retirement',
    jsonb_build_object('disabled_keys', jsonb_agg(key ORDER BY key)),
    'system', '{"migration_version":"20261013010000","retired":true}'::jsonb
FROM retired HAVING count(*) > 0;

UPDATE app_config SET value = to_char(clock_timestamp() AT TIME ZONE 'UTC',
    'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), updated_at = now()
WHERE key = 'update_time';

-- +goose Down
SELECT 1;
