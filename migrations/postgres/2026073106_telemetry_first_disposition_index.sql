-- +goose NO TRANSACTION
-- +goose Up

-- Lock/rewrite: the state table is empty until an application backfill starts.
-- The concurrent indexes scan their source tables without blocking normal
-- reads or writes. The state table is system-only because it coordinates a
-- global, non-user-visible recovery scan.
CREATE TABLE IF NOT EXISTS telemetry_first_disposition_backfill_state (
    state_key TEXT PRIMARY KEY,
    cursor_team_id UUID NULL,
    cursor_ingest_id UUID NULL,
    completed_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT telemetry_first_disposition_backfill_state_cursor_check CHECK (
        (cursor_team_id IS NULL AND cursor_ingest_id IS NULL)
        OR (cursor_team_id IS NOT NULL AND cursor_ingest_id IS NOT NULL)
    )
);

ALTER TABLE telemetry_first_disposition_backfill_state
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ NULL;

ALTER TABLE telemetry_first_disposition_backfill_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry_first_disposition_backfill_state FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS telemetry_first_disposition_backfill_state_system_access
    ON telemetry_first_disposition_backfill_state;
CREATE POLICY telemetry_first_disposition_backfill_state_system_access
    ON telemetry_first_disposition_backfill_state
    FOR ALL
    USING (current_setting('app.tx_mode', true) = 'system')
    WITH CHECK (current_setting('app.tx_mode', true) = 'system');

-- A failed concurrent build leaves an invalid index with the same name. A
-- replayed migration must remove that object before rebuilding it; a valid
-- partial build is also safe to replace because marker writers retain their
-- existing idempotency key during the rebuild.
DROP INDEX CONCURRENTLY IF EXISTS placement_outcomes_telemetry_first_disposition_unique;
CREATE UNIQUE INDEX CONCURRENTLY placement_outcomes_telemetry_first_disposition_unique
    ON placement_outcomes(team_id, placement_run_id)
    WHERE outcome_kind = 'telemetry_first_disposition';

-- The recovery scan is origin-scoped and keyset ordered by this partial index.
-- Legacy Remember ingests predate the explicit telemetry-origin marker, but
-- retain service-generated actor metadata matching the ingest owner.
DROP INDEX CONCURRENTLY IF EXISTS knowledge_ingests_telemetry_remember_backfill_idx;
CREATE INDEX CONCURRENTLY knowledge_ingests_telemetry_remember_backfill_idx
    ON knowledge_ingests(team_id, ingest_id)
    WHERE metadata ->> '_dense_mem_telemetry_origin' = 'remember'
       OR (
           NULLIF(metadata ->> 'contract_version', '') IS NOT NULL
           AND jsonb_typeof(metadata -> 'actor') = 'object'
           AND metadata #>> '{actor,team_id}' = team_id::text
           AND metadata #>> '{actor,profile_id}' = owner_profile_id::text
       );

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS knowledge_ingests_telemetry_remember_backfill_idx;
DROP INDEX CONCURRENTLY IF EXISTS placement_outcomes_telemetry_first_disposition_unique;
DROP TABLE IF EXISTS telemetry_first_disposition_backfill_state;
