-- +goose NO TRANSACTION

-- +goose Up
-- Lock/rewrite: all indexes are built concurrently and do not rewrite source rows.
-- RLS: these indexes do not change row visibility or transaction policy.
-- Recovery: invalid concurrent builds are parked before rebuilding the intended name.
-- Rollback: dropping these derived indexes is safe; lifecycle history remains authoritative.

DROP INDEX CONCURRENTLY IF EXISTS relationship_transition_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_correction_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_telemetry_status_invalid_idx;

-- +goose StatementBegin
DO $relationship_telemetry_invalid_indexes$
DECLARE
    invalid_index RECORD;
BEGIN
    FOR invalid_index IN
        SELECT namespace.nspname AS schema_name,
               index_class.relname AS index_name,
               CASE index_class.relname
                   WHEN 'relationship_transition_events_telemetry_window_idx' THEN 'relationship_transition_events_telemetry_window_invalid_idx'
                   WHEN 'relationship_correction_events_telemetry_window_idx' THEN 'relationship_correction_events_telemetry_window_invalid_idx'
                   WHEN 'relationship_records_telemetry_status_idx' THEN 'relationship_records_telemetry_status_invalid_idx'
               END AS replacement_name
        FROM pg_index AS index_state
        JOIN pg_class AS index_class ON index_class.oid = index_state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE NOT index_state.indisvalid
          AND index_class.relname IN (
              'relationship_transition_events_telemetry_window_idx',
              'relationship_correction_events_telemetry_window_idx',
              'relationship_records_telemetry_status_idx'
          )
    LOOP
        EXECUTE format('ALTER INDEX %I.%I RENAME TO %I', invalid_index.schema_name, invalid_index.index_name, invalid_index.replacement_name);
    END LOOP;
END
$relationship_telemetry_invalid_indexes$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_transition_events_telemetry_window_idx
    ON relationship_transition_events(created_at, team_id, owner_profile_id, to_status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_correction_events_telemetry_window_idx
    ON relationship_correction_events(created_at, team_id, owner_profile_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_records_telemetry_status_idx
    ON relationship_records(team_id, owner_profile_id, status);

DROP INDEX CONCURRENTLY IF EXISTS relationship_transition_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_correction_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_telemetry_status_invalid_idx;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS relationship_transition_events_telemetry_window_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_correction_events_telemetry_window_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_telemetry_status_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_transition_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_correction_events_telemetry_window_invalid_idx;
DROP INDEX CONCURRENTLY IF EXISTS relationship_records_telemetry_status_invalid_idx;
