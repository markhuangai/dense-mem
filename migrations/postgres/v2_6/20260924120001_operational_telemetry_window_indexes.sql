-- +goose NO TRANSACTION

-- +goose Up

-- Lock/rewrite impact: concurrent B-tree builds keep normal ledger writes available and do not rewrite source rows.
-- RLS impact: these derived indexes do not change row visibility or transaction policy.
-- Recovery: interrupted concurrent builds are renamed and dropped before rebuilding.
-- Rollback: dropping the indexes is safe; append-only ledger history remains authoritative.

SET lock_timeout = '30s';

DROP INDEX CONCURRENTLY IF EXISTS dream_cycle_runs_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS hypothesis_feedback_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_observations_telemetry_ingest_invalid_idx;

-- +goose StatementBegin
DO $operational_telemetry_window_invalid_indexes$
DECLARE
    invalid_index RECORD;
BEGIN
    FOR invalid_index IN
        SELECT namespace.nspname AS schema_name,
               index_class.relname AS index_name,
               CASE index_class.relname
                   WHEN 'dream_cycle_runs_telemetry_window_idx' THEN 'dream_cycle_runs_telemetry_window_invalid_idx'
                   WHEN 'hypothesis_feedback_events_telemetry_window_idx' THEN 'hypothesis_feedback_events_telemetry_window_invalid_idx'
                   WHEN 'relationship_observations_telemetry_ingest_idx' THEN 'relationship_observations_telemetry_ingest_invalid_idx'
               END AS replacement_name
        FROM pg_index AS index_state
        JOIN pg_class AS index_class ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND NOT index_state.indisvalid
          AND index_class.relname IN (
              'dream_cycle_runs_telemetry_window_idx',
              'hypothesis_feedback_events_telemetry_window_idx',
              'relationship_observations_telemetry_ingest_idx'
          )
    LOOP
        EXECUTE format('ALTER INDEX %I.%I RENAME TO %I', invalid_index.schema_name, invalid_index.index_name, invalid_index.replacement_name);
    END LOOP;
END
$operational_telemetry_window_invalid_indexes$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS dream_cycle_runs_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS hypothesis_feedback_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_observations_telemetry_ingest_invalid_idx;

CREATE INDEX CONCURRENTLY IF NOT EXISTS dream_cycle_runs_telemetry_window_idx
    ON dream_cycle_runs(started_at, team_id, space_id, space_generation)
    WHERE canonical_run_id IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS hypothesis_feedback_events_telemetry_window_idx
    ON hypothesis_feedback_events(created_at, team_id, space_id, space_generation)
    INCLUDE (decision);

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_observations_telemetry_ingest_idx
    ON relationship_observations(team_id, ingest_id, space_id, space_generation, relationship_id)
    WHERE relationship_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS dream_cycle_runs_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS hypothesis_feedback_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_observations_telemetry_ingest_invalid_idx;

RESET lock_timeout;

-- +goose Down

SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS dream_cycle_runs_telemetry_window_idx;
DROP INDEX CONCURRENTLY IF EXISTS hypothesis_feedback_events_telemetry_window_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_observations_telemetry_ingest_idx;
DROP INDEX CONCURRENTLY IF EXISTS dream_cycle_runs_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS hypothesis_feedback_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_observations_telemetry_ingest_invalid_idx;
RESET lock_timeout;
