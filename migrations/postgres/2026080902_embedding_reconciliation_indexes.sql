-- +goose NO TRANSACTION
-- +goose Up

-- Concurrent indexes keep the daily scan and operator projection from taking
-- an access-exclusive lock on the live embedding tables.
DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_reconciliation_failed_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_reconciliation_failed_idx
    ON embedding_jobs(
        embedding_contract_id, embedding_dimensions, status,
        failure_class, failure_code, updated_at, embedding_job_id
    )
    WHERE status = 'failed';

DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_reconciliation_team_idx;
CREATE INDEX CONCURRENTLY embedding_jobs_reconciliation_team_idx
    ON embedding_jobs(team_id, embedding_contract_id, embedding_dimensions,
                      status, source_kind, updated_at, embedding_job_id)
    WHERE status IN ('queued', 'processing', 'failed');

DROP INDEX CONCURRENTLY IF EXISTS embedding_failure_incidents_open_idx;
CREATE INDEX CONCURRENTLY embedding_failure_incidents_open_idx
    ON embedding_failure_incidents(
        embedding_contract_id, embedding_dimensions, status,
        failure_class, failure_code, last_seen_at DESC
    );

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS embedding_failure_incidents_open_idx;
DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_reconciliation_team_idx;
DROP INDEX CONCURRENTLY IF EXISTS embedding_jobs_reconciliation_failed_idx;
