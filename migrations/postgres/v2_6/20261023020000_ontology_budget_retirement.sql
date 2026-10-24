-- Lock/rewrite impact: five-second lock acquisition bound; nullable attempt columns avoid a heap rewrite.
-- Backfill: operational deferred-source updates only, atomically resumable by retrying this migration.
-- RLS impact: existing forced system/shared-space policies remain unchanged; migration authority is transaction-local.
-- Backward compatibility: pause or disable, drain, and stop old instances before applying; newer schema rejects old startup.
-- Rollback: history and conservative charges remain exact; populated retirement is irreversible and must roll forward.

-- +goose Up
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
-- +goose StatementBegin
DO $$
DECLARE admission_paused BOOLEAN;
BEGIN
 SELECT paused INTO admission_paused FROM ontology_maintenance_state WHERE singleton FOR UPDATE;
 IF NOT admission_paused AND EXISTS(SELECT 1 FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED' AND value='true') THEN
  RAISE EXCEPTION 'pause or disable and stop ontology workers before budget retirement';
 END IF;
 IF EXISTS(SELECT 1 FROM ontology_maintenance_batches WHERE status='running')
    OR EXISTS(SELECT 1 FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp())
    OR EXISTS(SELECT 1 FROM ontology_maintenance_attempts WHERE reconciled_at IS NULL) THEN
  RAISE EXCEPTION 'drain and recover unfinished ontology work before budget retirement';
 END IF;
END $$;
-- +goose StatementEnd

WITH closed AS (
 UPDATE ontology_maintenance_runs SET status='incomplete',failure_code='window_closed',updated_at=clock_timestamp()
 WHERE kind IN ('scheduled','run','retry') AND status IN ('pending','running') RETURNING run_id
)
INSERT INTO audit_log(operation,entity_type,entity_id,actor_role,metadata)
SELECT 'ONTOLOGY_BUDGET_RETIREMENT','ontology_maintenance',run_id::text,'system',
 '{"migration_version":"20261023020000","outcome":"window_closed"}'::jsonb FROM closed;

UPDATE ontology_maintenance_sources SET status='pending',updated_at=clock_timestamp()
WHERE status='budget_deferred';
ALTER TABLE ontology_maintenance_sources DROP CONSTRAINT ontology_maintenance_sources_status_check;
ALTER TABLE ontology_maintenance_sources ADD CONSTRAINT ontology_maintenance_sources_status_check
 CHECK(status IN ('pending','organized','ambiguous','failed','unavailable'));

ALTER TABLE ontology_maintenance_attempts
 ADD COLUMN estimated_input_tokens BIGINT NULL CHECK(estimated_input_tokens>0),
 ADD COLUMN max_output_tokens BIGINT NULL CHECK(max_output_tokens>0);
ALTER TABLE ontology_maintenance_attempts DROP CONSTRAINT ontology_maintenance_attempts_reserved_input_check;
ALTER TABLE ontology_maintenance_attempts DROP CONSTRAINT ontology_maintenance_attempts_reserved_output_check;
ALTER TABLE ontology_maintenance_attempts ADD CONSTRAINT ontology_maintenance_attempts_reserved_input_check CHECK(reserved_input>=0);
ALTER TABLE ontology_maintenance_attempts ADD CONSTRAINT ontology_maintenance_attempts_reserved_output_check CHECK(reserved_output>=0);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION dense_mem_guard_ontology_window() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF COALESCE((NEW.policy->>'context_window_tokens')::bigint,0) NOT BETWEEN 1 AND 1000000000
     OR NEW.policy ? 'input_tokens' OR NEW.policy ? 'output_tokens'
     OR ROW(NEW.charged_input,NEW.charged_output,NEW.reported_input,NEW.reported_output,NEW.reserved_input,NEW.reserved_output,NEW.additional_input,NEW.additional_output,NEW.overrun)
        IS DISTINCT FROM ROW(0::bigint,0::bigint,0::bigint,0::bigint,0::bigint,0::bigint,0::bigint,0::bigint,false) THEN
   RAISE EXCEPTION 'ontology spending budgets are retired; configure per-call context capacity';
  END IF;
 ELSIF TG_OP='DELETE' OR NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'ontology window identity, policy and historical accounting are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ontology_maintenance_window_admission_guard BEFORE INSERT ON ontology_maintenance_windows
 FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_window();

CREATE OR REPLACE FUNCTION dense_mem_guard_ontology_attempt() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.estimated_input_tokens IS NULL OR NEW.max_output_tokens IS NULL
     OR NEW.reserved_input<>0 OR NEW.reserved_output<>0 OR NEW.reconciled_at IS NOT NULL
     OR NEW.reported_input IS NOT NULL OR NEW.reported_output IS NOT NULL OR NEW.reported_total IS NOT NULL THEN
   RAISE EXCEPTION 'ontology spending reservations are retired; record per-call admission';
  END IF;
 ELSIF TG_OP='DELETE' OR OLD.reconciled_at IS NOT NULL OR NEW.reconciled_at IS NULL
    OR ROW(NEW.batch_id,NEW.assessment_id,NEW.attempt,NEW.window_id,NEW.reserved_input,NEW.reserved_output,NEW.admitted_at,NEW.estimated_input_tokens,NEW.max_output_tokens)
       IS DISTINCT FROM ROW(OLD.batch_id,OLD.assessment_id,OLD.attempt,OLD.window_id,OLD.reserved_input,OLD.reserved_output,OLD.admitted_at,OLD.estimated_input_tokens,OLD.max_output_tokens) THEN
  RAISE EXCEPTION 'ontology admission identity is immutable and terminal usage records once';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ontology_maintenance_attempt_admission_guard BEFORE INSERT ON ontology_maintenance_attempts
 FOR EACH ROW EXECUTE FUNCTION dense_mem_guard_ontology_attempt();

CREATE FUNCTION dense_mem_reject_ontology_allowance() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.kind='allowance' THEN RAISE EXCEPTION 'ontology allowance grants are retired'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ontology_allowance_retirement_guard BEFORE INSERT ON ontology_maintenance_runs
 FOR EACH ROW EXECUTE FUNCTION dense_mem_reject_ontology_allowance();
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM ontology_maintenance_windows)
    OR EXISTS(SELECT 1 FROM ontology_maintenance_runs)
    OR EXISTS(SELECT 1 FROM ontology_maintenance_sources)
    OR EXISTS(SELECT 1 FROM ontology_maintenance_teams WHERE lease_until IS NOT NULL) THEN
  RAISE EXCEPTION 'ontology budget retirement is populated; recover by rolling forward';
 END IF;
END $$;
DROP TRIGGER ontology_allowance_retirement_guard ON ontology_maintenance_runs;
DROP FUNCTION dense_mem_reject_ontology_allowance();
DROP TRIGGER ontology_maintenance_window_admission_guard ON ontology_maintenance_windows;
DROP TRIGGER ontology_maintenance_attempt_admission_guard ON ontology_maintenance_attempts;

CREATE OR REPLACE FUNCTION dense_mem_guard_ontology_window() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.window_id<>OLD.window_id OR NEW.starts_at<>OLD.starts_at
    OR NEW.ends_at<>OLD.ends_at OR NEW.policy<>OLD.policy THEN
  RAISE EXCEPTION 'ontology window policy and identity are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION dense_mem_guard_ontology_attempt() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.reconciled_at IS NOT NULL OR NEW.batch_id<>OLD.batch_id
    OR NEW.assessment_id<>OLD.assessment_id OR NEW.attempt<>OLD.attempt OR NEW.window_id<>OLD.window_id
    OR NEW.reserved_input<>OLD.reserved_input OR NEW.reserved_output<>OLD.reserved_output
    OR NEW.admitted_at<>OLD.admitted_at OR NEW.reconciled_at IS NULL THEN
  RAISE EXCEPTION 'ontology reservations are immutable and reconcile once';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
ALTER TABLE ontology_maintenance_attempts DROP COLUMN estimated_input_tokens,DROP COLUMN max_output_tokens;
ALTER TABLE ontology_maintenance_attempts DROP CONSTRAINT ontology_maintenance_attempts_reserved_input_check;
ALTER TABLE ontology_maintenance_attempts DROP CONSTRAINT ontology_maintenance_attempts_reserved_output_check;
ALTER TABLE ontology_maintenance_attempts ADD CONSTRAINT ontology_maintenance_attempts_reserved_input_check CHECK(reserved_input>0);
ALTER TABLE ontology_maintenance_attempts ADD CONSTRAINT ontology_maintenance_attempts_reserved_output_check CHECK(reserved_output>0);
ALTER TABLE ontology_maintenance_sources DROP CONSTRAINT ontology_maintenance_sources_status_check;
ALTER TABLE ontology_maintenance_sources ADD CONSTRAINT ontology_maintenance_sources_status_check
 CHECK(status IN ('pending','organized','ambiguous','failed','budget_deferred','unavailable'));
