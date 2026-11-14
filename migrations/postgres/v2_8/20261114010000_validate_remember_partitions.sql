-- Validation scans historical lifecycle rows under SHARE UPDATE EXCLUSIVE,
-- allowing ordinary readers and writers after the preceding DDL transaction.
-- No data is rewritten; cancellation leaves this validation safe to resume.
-- +goose Up
SET LOCAL lock_timeout = '30s';
ALTER TABLE evidence_lifecycle_events VALIDATE CONSTRAINT evidence_lifecycle_events_replacement_check;

-- +goose Down
-- The preceding migration owns removal and refuses to discard new provenance.
SELECT 1;
