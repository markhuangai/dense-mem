-- +goose NO TRANSACTION
-- +goose Up

-- Lock/rewrite: CREATE INDEX CONCURRENTLY avoids blocking writes; no heap rewrite occurs.
-- RLS: index-only structure change, no policy change.
CREATE INDEX CONCURRENTLY IF NOT EXISTS placement_runs_team_expired_claim_idx
    ON placement_runs(team_id, lease_until ASC, created_at ASC, placement_run_id)
    WHERE status = 'processing'
      AND lease_until IS NOT NULL
      AND attempts < max_attempts;

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS placement_runs_team_expired_claim_idx;
