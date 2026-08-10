-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_incident_resolution_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_incident_resolution_idx
    ON embedding_jobs(
        team_id, embedding_contract_id, embedding_dimensions,
        source_kind, failure_class, failure_code, status
    )
    WHERE status IN ('queued', 'processing', 'failed');

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_incident_resolution_idx;
