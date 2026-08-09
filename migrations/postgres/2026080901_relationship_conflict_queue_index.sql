-- +goose NO TRANSACTION

-- +goose Up
-- Lock/rewrite: the partial index is built concurrently, so active conflict
-- reads and writes continue while PostgreSQL scans existing rows.
-- RLS: these derived indexes add no policy and do not change row visibility.
-- Recovery: an interrupted concurrent build is renamed and dropped before a
-- valid replacement is created; queue data and history remain authoritative.
-- Rollback: dropping these derived indexes is safe; queue data and history remain.

DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_queue_active_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_ai_assessment_events_failed_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_resolution_plans_applied_invalid_idx;

-- CREATE INDEX CONCURRENTLY may leave an invalid catalog entry after failure.
-- Rename only that invalid entry so the concurrent drop can run outside a
-- transaction block, then rebuild the intended index below.
-- +goose StatementBegin
DO $relationship_conflict_queue_invalid_indexes$
DECLARE
    invalid_index RECORD;
BEGIN
    FOR invalid_index IN
        SELECT namespace.nspname AS schema_name,
               index_class.relname AS index_name,
               CASE index_class.relname
                   WHEN 'relationship_conflict_queue_active_idx' THEN 'relationship_conflict_queue_active_invalid_idx'
                   WHEN 'relationship_conflict_ai_assessment_events_failed_idx' THEN 'relationship_conflict_ai_assessment_events_failed_invalid_idx'
                   WHEN 'relationship_conflict_resolution_plans_applied_idx' THEN 'relationship_conflict_resolution_plans_applied_invalid_idx'
               END AS replacement_name
        FROM pg_index AS index_state
        JOIN pg_class AS index_class ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE NOT index_state.indisvalid
          AND index_class.relname IN (
              'relationship_conflict_queue_active_idx',
              'relationship_conflict_ai_assessment_events_failed_idx',
              'relationship_conflict_resolution_plans_applied_idx'
          )
    LOOP
        EXECUTE format('ALTER INDEX %I.%I RENAME TO %I', invalid_index.schema_name, invalid_index.index_name, invalid_index.replacement_name);
    END LOOP;
END
$relationship_conflict_queue_invalid_indexes$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_conflict_queue_active_idx
    ON relationship_conflict_cases (
        team_id,
        status DESC,
        next_review_at ASC,
        conflict_id ASC
    )
    WHERE status IN ('open', 'overdue');

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_conflict_ai_assessment_events_failed_idx
    ON relationship_conflict_ai_assessment_events(team_id, created_at)
    WHERE action = 'failed';

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_conflict_resolution_plans_applied_idx
    ON relationship_conflict_resolution_plans(team_id, applied_at)
    WHERE status = 'applied' AND method = 'last_write_wins';

DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_queue_active_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_ai_assessment_events_failed_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_resolution_plans_applied_invalid_idx;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_queue_active_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_ai_assessment_events_failed_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_conflict_resolution_plans_applied_idx;
