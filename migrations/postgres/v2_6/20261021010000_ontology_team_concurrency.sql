-- Lock/rewrite impact: replaces a small partial operational index; no canonical rewrite or backfill.
-- RLS impact: unchanged; existing forced shared-space policies govern every batch.
-- Backward compatibility: upgrade and rollback require paused admission, drained work, and old workers stopped.
-- History and immutable window policies are retained; missing per-team settings mean one.

-- +goose Up
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
DROP INDEX ontology_maintenance_batch_team_idx;
CREATE INDEX ontology_maintenance_batch_team_idx
    ON ontology_maintenance_batches(team_id,shared_space_id,space_generation,lease_until)
    WHERE status='running';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'migration', true);
DO $guard$
BEGIN
    IF EXISTS(SELECT 1 FROM ontology_maintenance_batches WHERE status='running')
       OR EXISTS(SELECT 1 FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp()) THEN
        RAISE EXCEPTION 'drain ontology work before reverting team concurrency';
    END IF;
END $guard$;
DROP INDEX ontology_maintenance_batch_team_idx;
CREATE UNIQUE INDEX ontology_maintenance_batch_team_idx
    ON ontology_maintenance_batches(team_id,shared_space_id,space_generation) WHERE status='running';
-- +goose StatementEnd
