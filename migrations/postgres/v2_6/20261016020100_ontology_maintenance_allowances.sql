-- Lock/rewrite impact: brief ALTER TABLE locks, five-second lock timeout, constant defaults without heap rewrite.
-- RLS impact: retain existing operator-only forced RLS; no new request-visible tables.
-- Backfill: none; existing windows receive zero additional allowance.
-- Backward compatibility: deploy matching maintenance readers and writers; original policy and usage remain unchanged.
-- Rollback: refuse Down after a grant; disable maintenance and roll forward to retain authorization history.

-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE ontology_maintenance_windows
 ADD COLUMN additional_input BIGINT NOT NULL DEFAULT 0 CHECK (additional_input BETWEEN 0 AND 1000000000),
 ADD COLUMN additional_output BIGINT NOT NULL DEFAULT 0 CHECK (additional_output BETWEEN 0 AND 1000000000);
ALTER TABLE ontology_maintenance_runs DROP CONSTRAINT ontology_maintenance_runs_kind_check;
ALTER TABLE ontology_maintenance_runs ADD CONSTRAINT ontology_maintenance_runs_kind_check
 CHECK (kind IN ('scheduled','run','retry','pause','resume','allowance'));
-- +goose StatementBegin
CREATE FUNCTION dense_mem_guard_ontology_allowance() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.kind='allowance' OR (TG_OP='UPDATE' AND NEW.kind='allowance') THEN
  RAISE EXCEPTION 'ontology allowance history is immutable';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER ontology_allowance_guard BEFORE UPDATE OR DELETE ON ontology_maintenance_runs
 FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_allowance();

-- +goose Down
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM ontology_maintenance_runs WHERE kind='allowance') THEN
  RAISE EXCEPTION 'ontology allowance history is populated; recover by rolling forward';
 END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER ontology_allowance_guard ON ontology_maintenance_runs;
DROP FUNCTION dense_mem_guard_ontology_allowance();
ALTER TABLE ontology_maintenance_runs DROP CONSTRAINT ontology_maintenance_runs_kind_check;
ALTER TABLE ontology_maintenance_runs ADD CONSTRAINT ontology_maintenance_runs_kind_check
 CHECK (kind IN ('scheduled','run','retry','pause','resume'));
ALTER TABLE ontology_maintenance_windows DROP COLUMN additional_input, DROP COLUMN additional_output;
