-- +goose NO TRANSACTION

-- +goose Up
-- Lock/rewrite: the partial index is built concurrently, so active conflict
-- reads and writes continue while PostgreSQL scans existing rows.
-- Rollback: dropping this derived index is safe; queue data and history remain.
CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_conflict_queue_active_idx
    ON relationship_conflict_cases (
        team_id,
        status DESC,
        next_review_at ASC,
        conflict_id ASC
    )
    WHERE status IN ('open', 'overdue');

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_queue_active_idx;
