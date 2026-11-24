-- Lock/rewrite impact: validates the status check by scanning under SHARE UPDATE EXCLUSIVE, with no row rewrite.
-- RLS impact: existing policies and request scopes remain in force during validation.
-- Backfill: existing rows already meet the widened check. Retry validation if interrupted.
-- Backward compatibility: the preceding migration already enforces the same check on new writes.
-- Rollback: Down is a no-op and leaves the enforced constraint in place.
-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE topic_maintenance_sources VALIDATE CONSTRAINT topic_maintenance_sources_status_check;

-- +goose Down
SELECT 1;
