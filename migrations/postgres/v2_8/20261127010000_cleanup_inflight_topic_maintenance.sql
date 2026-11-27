-- Lock/rewrite impact: row locks on interrupted maintenance work; no table rewrite.
-- RLS impact: uses the existing transaction-local migration policies and changes no policies.
-- Backfill: terminalizes interrupted batches and attempts and releases their coordination leases.
-- Backward compatibility: requires stopped application writers and keeps maintenance history.
-- Rollback: roll forward or restore the verified offline backup before reopening writes.

-- +goose Up
SET LOCAL app.tx_mode = 'migration';
SET LOCAL lock_timeout = '5s';

UPDATE topic_maintenance_batches
SET status = 'lost',
    failure_code = CASE WHEN failure_code = '' THEN 'upgrade_interrupted' ELSE failure_code END,
    completed_at = COALESCE(completed_at, clock_timestamp())
WHERE status = 'running';

UPDATE topic_maintenance_teams
SET lease_token = NULL, lease_until = NULL
WHERE lease_token IS NOT NULL OR lease_until IS NOT NULL;

UPDATE topic_maintenance_community_work
SET lease_batch_id = NULL, lease_until = NULL
WHERE lease_batch_id IS NOT NULL OR lease_until IS NOT NULL;

UPDATE topic_embeddings
SET consolidation_batch_id = NULL, consolidation_lease_until = NULL
WHERE consolidation_batch_id IS NOT NULL OR consolidation_lease_until IS NOT NULL;

UPDATE topic_maintenance_attempts
SET reported_input = COALESCE(reported_input, 0),
    reported_output = COALESCE(reported_output, 0),
    reported_total = COALESCE(reported_total, 0),
    reconciled_at = clock_timestamp()
WHERE reconciled_at IS NULL;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'topic maintenance upgrade cleanup requires forward recovery';
END $$;
-- +goose StatementEnd
