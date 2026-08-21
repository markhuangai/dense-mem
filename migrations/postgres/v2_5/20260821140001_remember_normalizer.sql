-- +goose Up
-- +goose StatementBegin

-- Remember normalizer v1 is a restart boundary. Unfinished placement work is
-- made terminal before the obsolete review/hold projections are removed; a
-- caller can resubmit the complete batch with a new idempotency key.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Remove the deferred guard before changing placement_runs. PostgreSQL cannot
-- alter a table while that guard has pending trigger events in the transaction.
DROP TRIGGER IF EXISTS placement_runs_submission_hold_guard ON placement_runs;
DROP FUNCTION IF EXISTS ensure_submission_hold_for_awaiting_review();

-- Capture and close review tasks attached to work that this restart discards.
-- Keep unrelated correction, conflict, and Dream tasks available.
WITH restarted_runs AS MATERIALIZED (
    SELECT run.team_id, run.placement_run_id, run.ingest_id
    FROM placement_runs AS run
    WHERE run.status IN ('queued', 'guarded', 'processing', 'awaiting_review')
)
UPDATE review_tasks AS task
SET status = 'canceled',
    reason = 'remember_normalizer_restart',
    resolution = jsonb_build_object('reason', 'remember_normalizer_restart'),
    resolved_at = COALESCE(task.resolved_at, now()),
    updated_at = now()
WHERE task.status IN ('open', 'acknowledged')
  AND (
      EXISTS (
          SELECT 1
          FROM placement_items AS item
          JOIN restarted_runs AS run
            ON run.team_id = item.team_id
           AND run.placement_run_id = item.placement_run_id
          WHERE item.team_id = task.team_id
            AND item.placement_item_id = task.placement_item_id
      )
      OR EXISTS (
          SELECT 1
          FROM restarted_runs AS run
          WHERE run.team_id = task.team_id
            AND run.ingest_id = task.ingest_id
      )
  );

-- The placement run is the public processing authority, but the ingest row is
-- also queried by maintenance and diagnostics. Keep both authorities terminal.
UPDATE knowledge_ingests AS ingest
SET status = 'failed',
    error = 'remember normalizer restarted; resubmit the complete batch',
    completed_at = COALESCE(ingest.completed_at, now()),
    updated_at = now()
WHERE EXISTS (
    SELECT 1
    FROM placement_runs AS run
    WHERE run.team_id = ingest.team_id
      AND run.ingest_id = ingest.ingest_id
      AND run.status IN ('queued', 'guarded', 'processing', 'awaiting_review')
);

-- Clear item-level assessor claims, including completed siblings retained from
-- a partially processed legacy run.
UPDATE placement_items AS item
SET assessor_attempt_id = NULL,
    assessor_attempted_at = NULL,
    updated_at = now()
WHERE EXISTS (
    SELECT 1
    FROM placement_runs AS run
    WHERE run.team_id = item.team_id
      AND run.placement_run_id = item.placement_run_id
      AND run.status IN ('queued', 'guarded', 'processing', 'awaiting_review')
);

DO $dense_mem_fail_unfinished_remember$
BEGIN
    UPDATE placement_items AS item
    SET status = 'failed',
        category = 'failed',
        assessor_attempt_id = NULL,
        assessor_attempted_at = NULL,
        version = version + 1,
        result = jsonb_build_object(
            'failure_stage', 'normalization_failed',
            'failure_code', 'submission_requires_resubmission',
            'retryable', true,
            'next_action', 'resubmit_submission',
            'reason', 'remember normalizer restarted'
        ),
        error = 'remember normalizer restarted; resubmit the complete batch',
        updated_at = now()
    WHERE item.status IN ('queued', 'processing', 'awaiting_review');

    UPDATE placement_runs AS run
    SET status = 'failed',
        error = 'remember normalizer restarted; resubmit the complete batch',
        worker_id = '',
        lease_until = NULL,
        assessor_attempt_id = NULL,
        assessor_attempted_at = NULL,
        completed_at = COALESCE(run.completed_at, now()),
        updated_at = now()
    WHERE run.status IN ('queued', 'guarded', 'processing', 'awaiting_review');

END;
$dense_mem_fail_unfinished_remember$;

-- Remember no longer creates semantic review work. The shared review_tasks
-- table remains because correction, conflict, and Dream workflows still use
-- it; this migration only removes the Remember hold projection.
DROP INDEX IF EXISTS submission_holds_owner_expiry_idx;
DROP INDEX IF EXISTS submission_holds_expiry_idx;
DROP INDEX IF EXISTS placement_runs_active_replacement_unique;
DROP INDEX IF EXISTS placement_runs_replacement_target_idx;
DROP TABLE IF EXISTS submission_holds;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_semantic_hold_version_check,
    DROP CONSTRAINT IF EXISTS placement_runs_semantic_hold_state_check,
    DROP CONSTRAINT IF EXISTS placement_runs_replaces_run_ref,
    DROP CONSTRAINT IF EXISTS placement_runs_superseded_by_run_ref;
ALTER TABLE placement_runs
    DROP COLUMN IF EXISTS semantic_hold_state,
    DROP COLUMN IF EXISTS semantic_hold_version,
    DROP COLUMN IF EXISTS semantic_hold_updated_at,
    DROP COLUMN IF EXISTS replaces_placement_run_id,
    DROP COLUMN IF EXISTS superseded_by_placement_run_id;

-- The old schema's status constraints are rewritten without awaiting_review.
ALTER TABLE placement_runs DROP CONSTRAINT IF EXISTS placement_runs_status_check;
ALTER TABLE placement_runs ADD CONSTRAINT placement_runs_status_check
    CHECK (status IN ('queued', 'guarded', 'quarantined', 'processing', 'completed', 'failed'));
ALTER TABLE placement_runs DROP CONSTRAINT IF EXISTS placement_runs_completion_check;
ALTER TABLE placement_runs ADD CONSTRAINT placement_runs_completion_check
    CHECK (
        (status IN ('completed', 'failed', 'quarantined') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed', 'failed', 'quarantined'))
    );
ALTER TABLE placement_items DROP CONSTRAINT IF EXISTS placement_items_status_check;
ALTER TABLE placement_items ADD CONSTRAINT placement_items_status_check
    CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'quarantined'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $dense_mem_irreversible_remember_normalizer$
BEGIN
    RAISE EXCEPTION '20260821140001 is irreversible: restart and resubmit unfinished Remember submissions';
END;
$dense_mem_irreversible_remember_normalizer$;
-- +goose StatementEnd
