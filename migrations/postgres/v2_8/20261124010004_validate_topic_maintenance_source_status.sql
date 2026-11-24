-- Validate the widened status check under SHARE UPDATE EXCLUSIVE without
-- rewriting rows or changing RLS. The enforced predecessor admitted a subset.
-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE topic_maintenance_sources VALIDATE CONSTRAINT topic_maintenance_sources_status_check;

-- +goose Down
SELECT 1;
