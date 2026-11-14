-- Lock/rewrite impact: scan lifecycle rows under SHARE UPDATE EXCLUSIVE after the
-- preceding DDL transaction; ordinary reads/writes continue and no rows are rewritten.
-- RLS impact: validation changes no row policies or request scopes.
-- Backfill: none; interrupted constraint validation is safe to resume.
-- Backward compatibility: preserve both constraints and their new-write enforcement.
-- Rollback: Down is a no-op; the preceding migration owns removal and refuses to
-- discard populated provenance.
-- +goose Up
SET LOCAL lock_timeout = '30s';
ALTER TABLE evidence_lifecycle_events VALIDATE CONSTRAINT evidence_lifecycle_events_replacement_check;
ALTER TABLE evidence_lifecycle_events VALIDATE CONSTRAINT evidence_lifecycle_replacement_item_fk;

-- +goose Down
-- The preceding migration owns removal and refuses to discard new provenance.
SELECT 1;
