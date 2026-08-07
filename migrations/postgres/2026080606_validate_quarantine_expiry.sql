-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Migration 0604 committed the NOT VALID constraint separately so its
-- ACCESS EXCLUSIVE add-constraint lock is not held during this backfill or
-- validation scan. The update is idempotent for interrupted deployments.
UPDATE placement_runs
SET quarantine_expires_at = COALESCE(completed_at, created_at) + interval '24 hours'
WHERE status = 'quarantined'
  AND quarantine_expires_at IS NULL;

ALTER TABLE placement_runs
    VALIDATE CONSTRAINT placement_runs_quarantine_expiry_check;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_irreversible_quarantine_expiry_validation$
BEGIN
    RAISE EXCEPTION 'irreversible migration: quarantine expiry backfill and validation cannot be rolled back';
END
$dense_mem_irreversible_quarantine_expiry_validation$;
-- +goose StatementEnd
