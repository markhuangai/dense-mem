-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - The projection columns and append-only hold ledger are additive. The
--   partial successor index takes a short table lock while it is created.
-- - Existing submission-scoped awaiting_review runs are backfilled only when
--   every item has the exact #151 hold shape. Ambiguous rows abort the
--   migration rather than being silently coerced.
-- - The deferred guard prevents a future submission run from reaching
--   awaiting_review without a durable hold in the same transaction.
-- - Down refuses after any hold or hold projection exists; hold history is not
--   recoverable by removing the schema.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE placement_runs
    ADD COLUMN IF NOT EXISTS semantic_hold_state TEXT NULL,
    ADD COLUMN IF NOT EXISTS semantic_hold_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS semantic_hold_updated_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS replaces_placement_run_id UUID NULL,
    ADD COLUMN IF NOT EXISTS superseded_by_placement_run_id UUID NULL;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_semantic_hold_state_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_semantic_hold_state_check CHECK (
        semantic_hold_state IS NULL
        OR semantic_hold_state IN ('active', 'expired', 'superseded')
    );

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_semantic_hold_version_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_semantic_hold_version_check CHECK (semantic_hold_version >= 0);

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_replaces_run_ref;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_replaces_run_ref
    FOREIGN KEY (team_id, replaces_placement_run_id, owner_profile_id)
    REFERENCES placement_runs(team_id, placement_run_id, owner_profile_id)
    ON DELETE RESTRICT;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_superseded_by_run_ref;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_superseded_by_run_ref
    FOREIGN KEY (team_id, superseded_by_placement_run_id, owner_profile_id)
    REFERENCES placement_runs(team_id, placement_run_id, owner_profile_id)
    ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS placement_runs_replacement_target_idx
    ON placement_runs(team_id, replaces_placement_run_id, created_at ASC, placement_run_id)
    WHERE replaces_placement_run_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS placement_runs_active_replacement_unique
    ON placement_runs(team_id, replaces_placement_run_id)
    WHERE replaces_placement_run_id IS NOT NULL
      AND status IN ('queued', 'guarded', 'processing');

CREATE TABLE IF NOT EXISTS submission_holds (
    team_id UUID NOT NULL,
    submission_hold_id UUID NOT NULL DEFAULT gen_random_uuid(),
    placement_run_id UUID NOT NULL,
    ingest_id UUID NOT NULL,
    assessment_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    reason_code TEXT NOT NULL,
    held_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, submission_hold_id),
    UNIQUE (team_id, placement_run_id),
    UNIQUE (team_id, assessment_id),
    FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id)
        REFERENCES placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (team_id, assessment_id)
        REFERENCES placement_assessments(team_id, assessment_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES semantic_profile_refs(team_id, profile_id)
        ON DELETE RESTRICT,
    CONSTRAINT submission_holds_reason_nonempty CHECK (btrim(reason_code) <> ''),
    CONSTRAINT submission_holds_expiry_check CHECK (expires_at = held_at + interval '24 hours'),
    CONSTRAINT submission_holds_time_order_check CHECK (expires_at > held_at)
);

CREATE INDEX IF NOT EXISTS submission_holds_owner_expiry_idx
    ON submission_holds(team_id, owner_profile_id, expires_at ASC, placement_run_id);

CREATE INDEX IF NOT EXISTS submission_holds_expiry_idx
    ON submission_holds(team_id, expires_at ASC, placement_run_id);

DROP TRIGGER IF EXISTS submission_holds_append_only ON submission_holds;
CREATE TRIGGER submission_holds_append_only
    BEFORE UPDATE OR DELETE ON submission_holds
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE submission_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE submission_holds FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS submission_holds_select ON submission_holds;
CREATE POLICY submission_holds_select ON submission_holds
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS submission_holds_insert ON submission_holds;
CREATE POLICY submission_holds_insert ON submission_holds
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );

DO $dense_mem_submission_hold_backfill_preflight$
DECLARE
    invalid_count BIGINT;
BEGIN
    SELECT count(*)
    INTO invalid_count
    FROM placement_runs AS run
    JOIN placement_assessments AS assessment
      ON assessment.team_id = run.team_id
     AND assessment.placement_run_id = run.placement_run_id
     AND assessment.ingest_id = run.ingest_id
     AND assessment.owner_profile_id = run.owner_profile_id
     AND assessment.assessment_scope = 'submission'
    WHERE run.status = 'awaiting_review'
      AND NOT EXISTS (
          SELECT 1
          FROM submission_holds AS existing_hold
          WHERE existing_hold.team_id = run.team_id
            AND existing_hold.placement_run_id = run.placement_run_id
      )
      AND (
          run.completed_at IS NULL
          OR
          NOT EXISTS (
              SELECT 1
              FROM placement_items AS item
              WHERE item.team_id = run.team_id
                AND item.placement_run_id = run.placement_run_id
          )
          OR EXISTS (
              SELECT 1
              FROM placement_items AS item
              WHERE item.team_id = run.team_id
                AND item.placement_run_id = run.placement_run_id
                AND (item.status <> 'awaiting_review' OR item.category <> 'candidate')
          )
          OR EXISTS (
              SELECT 1
              FROM review_tasks AS task
              JOIN placement_items AS item
                ON item.team_id = task.team_id
               AND item.placement_item_id = task.placement_item_id
              WHERE item.team_id = run.team_id
                AND item.placement_run_id = run.placement_run_id
          )
          OR EXISTS (
              SELECT 1
              FROM placement_outcomes AS outcome
              JOIN placement_items AS item
                ON item.team_id = outcome.team_id
               AND item.placement_item_id = outcome.placement_item_id
              WHERE outcome.team_id = run.team_id
                AND outcome.placement_run_id = run.placement_run_id
                AND item.placement_run_id = run.placement_run_id
                AND outcome.status <> 'review_required'
          )
          OR EXISTS (
              SELECT 1
              FROM (
                  SELECT outcome.payload ->> 'failure_stage' AS failure_stage
                  FROM placement_outcomes AS outcome
                  JOIN placement_items AS item
                    ON item.team_id = outcome.team_id
                   AND item.placement_item_id = outcome.placement_item_id
                  WHERE outcome.team_id = run.team_id
                    AND outcome.placement_run_id = run.placement_run_id
                    AND item.placement_run_id = run.placement_run_id
                  GROUP BY outcome.payload ->> 'failure_stage'
              ) AS stages
              HAVING count(*) > 1
          )
          OR (
              (SELECT count(*)
               FROM placement_outcomes AS outcome
               JOIN placement_items AS item
                 ON item.team_id = outcome.team_id
                AND item.placement_item_id = outcome.placement_item_id
               WHERE outcome.team_id = run.team_id
                 AND outcome.placement_run_id = run.placement_run_id
                 AND item.placement_run_id = run.placement_run_id)
              <> (SELECT count(*)
                  FROM placement_items AS item
                  WHERE item.team_id = run.team_id
                    AND item.placement_run_id = run.placement_run_id)
          )
      );

    IF invalid_count > 0 THEN
        RAISE EXCEPTION
            'cannot apply 2026080601: % submission awaiting_review runs do not have the exact semantic-hold shape',
            invalid_count;
    END IF;
END
$dense_mem_submission_hold_backfill_preflight$;

INSERT INTO submission_holds (
    team_id, placement_run_id, ingest_id, assessment_id, owner_profile_id,
    reason_code, held_at, expires_at
)
SELECT
    run.team_id,
    run.placement_run_id,
    run.ingest_id,
    assessment.assessment_id,
    run.owner_profile_id,
    COALESCE(NULLIF(min(outcome.payload ->> 'failure_stage'), ''), 'policy_review'),
    run.completed_at,
    run.completed_at + interval '24 hours'
FROM placement_runs AS run
JOIN placement_assessments AS assessment
  ON assessment.team_id = run.team_id
 AND assessment.placement_run_id = run.placement_run_id
 AND assessment.ingest_id = run.ingest_id
 AND assessment.owner_profile_id = run.owner_profile_id
 AND assessment.assessment_scope = 'submission'
JOIN placement_items AS item
  ON item.team_id = run.team_id
 AND item.placement_run_id = run.placement_run_id
JOIN placement_outcomes AS outcome
  ON outcome.team_id = item.team_id
 AND outcome.placement_run_id = item.placement_run_id
 AND outcome.placement_item_id = item.placement_item_id
WHERE run.status = 'awaiting_review'
GROUP BY run.team_id, run.placement_run_id, run.ingest_id,
         assessment.assessment_id, run.owner_profile_id, run.completed_at
HAVING count(*) = count(DISTINCT item.placement_item_id)
   AND bool_and(item.status = 'awaiting_review' AND item.category = 'candidate')
   AND bool_and(outcome.status = 'review_required');

UPDATE placement_runs AS run
SET semantic_hold_state = CASE
        WHEN hold.expires_at <= clock_timestamp() THEN 'expired'
        ELSE 'active'
    END,
    semantic_hold_version = 1,
    semantic_hold_updated_at = CASE
        WHEN hold.expires_at <= clock_timestamp() THEN clock_timestamp()
        ELSE run.completed_at
    END
FROM submission_holds AS hold
WHERE hold.team_id = run.team_id
  AND hold.placement_run_id = run.placement_run_id
  AND run.semantic_hold_state IS NULL;

INSERT INTO placement_outcomes (
    team_id, placement_run_id, placement_item_id, owner_profile_id,
    outcome_kind, status, idempotency_key, payload
)
SELECT
    hold.team_id,
    hold.placement_run_id,
    NULL,
    hold.owner_profile_id,
    'submission_hold_created',
    'active',
    'submission-hold:' || hold.placement_run_id::text || ':created',
    jsonb_build_object(
        'reason_code', hold.reason_code,
        'held_at', hold.held_at,
        'expires_at', hold.expires_at,
        'backfilled', true
    )
FROM submission_holds AS hold
ON CONFLICT DO NOTHING;

INSERT INTO placement_outcomes (
    team_id, placement_run_id, placement_item_id, owner_profile_id,
    outcome_kind, status, idempotency_key, payload
)
SELECT
    hold.team_id,
    hold.placement_run_id,
    NULL,
    hold.owner_profile_id,
    'submission_hold_expired',
    'expired',
    'submission-hold:' || hold.placement_run_id::text || ':expired',
    jsonb_build_object(
        'reason_code', hold.reason_code,
        'held_at', hold.held_at,
        'expires_at', hold.expires_at,
        'backfilled', true
    )
FROM submission_holds AS hold
JOIN placement_runs AS run
  ON run.team_id = hold.team_id
 AND run.placement_run_id = hold.placement_run_id
WHERE run.semantic_hold_state = 'expired'
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION ensure_submission_hold_for_awaiting_review()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status = 'awaiting_review'
       AND EXISTS (
           SELECT 1
           FROM placement_assessments AS assessment
           WHERE assessment.team_id = NEW.team_id
             AND assessment.placement_run_id = NEW.placement_run_id
             AND assessment.ingest_id = NEW.ingest_id
             AND assessment.owner_profile_id = NEW.owner_profile_id
             AND assessment.assessment_scope = 'submission'
       )
       AND NOT EXISTS (
           SELECT 1
           FROM submission_holds AS hold
           WHERE hold.team_id = NEW.team_id
             AND hold.placement_run_id = NEW.placement_run_id
       )
    THEN
        RAISE EXCEPTION 'submission placement run % requires a durable hold before awaiting_review', NEW.placement_run_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS placement_runs_submission_hold_guard ON placement_runs;
CREATE CONSTRAINT TRIGGER placement_runs_submission_hold_guard
    AFTER INSERT OR UPDATE OF status, ingest_id, owner_profile_id
    ON placement_runs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ensure_submission_hold_for_awaiting_review();

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $dense_mem_submission_hold_rollback_preflight$
DECLARE
    hold_count BIGINT;
    projected_count BIGINT;
BEGIN
    SELECT count(*) INTO hold_count FROM submission_holds;
    SELECT count(*) INTO projected_count
    FROM placement_runs
    WHERE semantic_hold_state IS NOT NULL
       OR replaces_placement_run_id IS NOT NULL
       OR superseded_by_placement_run_id IS NOT NULL;
    IF hold_count > 0 OR projected_count > 0 THEN
        RAISE EXCEPTION
            'cannot roll back 2026080601: semantic hold history (%) or projections (%) exist',
            hold_count,
            projected_count;
    END IF;
END
$dense_mem_submission_hold_rollback_preflight$;

DROP TRIGGER IF EXISTS placement_runs_submission_hold_guard ON placement_runs;
DROP FUNCTION IF EXISTS ensure_submission_hold_for_awaiting_review();

DROP TABLE IF EXISTS submission_holds;

DROP INDEX IF EXISTS placement_runs_active_replacement_unique;
DROP INDEX IF EXISTS placement_runs_replacement_target_idx;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_replaces_run_ref,
    DROP CONSTRAINT IF EXISTS placement_runs_superseded_by_run_ref,
    DROP CONSTRAINT IF EXISTS placement_runs_semantic_hold_version_check,
    DROP CONSTRAINT IF EXISTS placement_runs_semantic_hold_state_check,
    DROP COLUMN IF EXISTS semantic_hold_state,
    DROP COLUMN IF EXISTS semantic_hold_version,
    DROP COLUMN IF EXISTS semantic_hold_updated_at,
    DROP COLUMN IF EXISTS replaces_placement_run_id,
    DROP COLUMN IF EXISTS superseded_by_placement_run_id;

-- +goose StatementEnd
