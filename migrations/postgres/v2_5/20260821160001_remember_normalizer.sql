-- Lock/rewrite impact: placement_runs and placement_items take ACCESS EXCLUSIVE
-- locks while status and legacy-column constraints are replaced. The data
-- updates lock only unfinished legacy rows and do not rewrite accepted semantic
-- history. Run this migration while Remember traffic is stopped.
-- RLS impact: migration mode is explicit. Existing placement and review-task
-- policies remain in place; obsolete hold-table policies disappear with the
-- table.
-- Backfill: every legacy unfinished placement submission becomes failed and
-- requires full resubmission with a new idempotency key. Completed item siblings
-- stay completed. Open or acknowledged placement review tasks attached to those
-- discarded submissions are canceled.
-- Correction submissions and relationship conflict cases are not modified.
-- Backward compatibility: this is a one-way V2.5 restart boundary. No legacy
-- queue or awaiting-review work is resumed.
-- Rollback: irreversible. Reopening discarded work would invent workflow
-- history after callers may already have resubmitted it.

-- +goose Up
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

-- Remove the deferred guard before changing placement_runs. PostgreSQL cannot
-- alter a table while this trigger has pending events in the transaction.
DROP TRIGGER IF EXISTS placement_runs_submission_hold_guard ON placement_runs;
DROP FUNCTION IF EXISTS ensure_submission_hold_for_awaiting_review();

-- review_tasks is retained only as inert audit history. Cancel only placement
-- rows attached to work discarded by this restart; correction and conflict
-- state lives in dedicated tables and is intentionally untouched.
WITH affected_runs AS MATERIALIZED (
    SELECT run.team_id, run.placement_run_id, run.ingest_id
    FROM placement_runs AS run
    WHERE run.status IN ('queued', 'guarded', 'processing', 'awaiting_review')
       OR EXISTS (
           SELECT 1
           FROM placement_items AS item
           WHERE item.team_id = run.team_id
             AND item.placement_run_id = run.placement_run_id
             AND item.status = 'awaiting_review'
       )
)
UPDATE review_tasks AS task
SET status = 'canceled',
    reason = 'legacy_placement_review_removed',
    resolution = jsonb_build_object(
        'reason', 'legacy_placement_review_removed',
        'next_action', 'resubmit_submission'
    ),
    resolved_at = COALESCE(task.resolved_at, now()),
    updated_at = now()
WHERE task.status IN ('open', 'acknowledged')
  AND task.placement_item_id IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM placement_items AS item
      JOIN affected_runs AS affected
        ON affected.team_id = item.team_id
       AND affected.placement_run_id = item.placement_run_id
      WHERE item.team_id = task.team_id
        AND item.placement_item_id = task.placement_item_id
  );

-- The service restart makes every nonterminal run a legacy queue entry. Also
-- include inconsistent terminal runs that still contain an awaiting-review
-- item so no removed workflow state survives behind a terminal parent.
WITH affected_runs AS MATERIALIZED (
    SELECT run.team_id, run.placement_run_id, run.ingest_id
    FROM placement_runs AS run
    WHERE run.status IN ('queued', 'guarded', 'processing', 'awaiting_review')
       OR EXISTS (
           SELECT 1
           FROM placement_items AS item
           WHERE item.team_id = run.team_id
             AND item.placement_run_id = run.placement_run_id
             AND item.status = 'awaiting_review'
       )
), failed_ingests AS (
    UPDATE knowledge_ingests AS ingest
    SET status = 'failed',
        error = 'legacy remember processing removed; resubmit the complete submission',
        completed_at = COALESCE(ingest.completed_at, now()),
        updated_at = now()
    WHERE EXISTS (
        SELECT 1
        FROM affected_runs AS affected
        WHERE affected.team_id = ingest.team_id
          AND affected.ingest_id = ingest.ingest_id
    )
    RETURNING ingest.team_id, ingest.ingest_id
), failed_items AS (
    UPDATE placement_items AS item
    SET status = CASE
            WHEN item.status IN ('queued', 'processing', 'awaiting_review') THEN 'failed'
            ELSE item.status
        END,
        category = CASE
            WHEN item.status IN ('queued', 'processing', 'awaiting_review') THEN 'failed'
            ELSE item.category
        END,
        assessor_attempt_id = NULL,
        assessor_attempted_at = NULL,
        version = item.version + CASE
            WHEN item.status IN ('queued', 'processing', 'awaiting_review') THEN 1
            ELSE 0
        END,
        result = CASE
            WHEN item.status IN ('queued', 'processing', 'awaiting_review') THEN jsonb_build_object(
                'failure_stage', 'normalization_failed',
                'failure_code', 'submission_requires_resubmission',
                'retryable', true,
                'next_action', 'resubmit_submission',
                'reason', 'legacy remember processing removed'
            )
            ELSE item.result
        END,
        error = CASE
            WHEN item.status IN ('queued', 'processing', 'awaiting_review')
                THEN 'legacy remember processing removed; resubmit the complete submission'
            ELSE item.error
        END,
        updated_at = now()
    WHERE EXISTS (
        SELECT 1
        FROM affected_runs AS affected
        WHERE affected.team_id = item.team_id
          AND affected.placement_run_id = item.placement_run_id
    )
    RETURNING item.team_id, item.placement_run_id
)
UPDATE placement_runs AS run
SET status = 'failed',
    error = 'legacy remember processing removed; resubmit the complete submission',
    worker_id = '',
    lease_until = NULL,
    assessor_attempt_id = NULL,
    assessor_attempted_at = NULL,
    completed_at = COALESCE(run.completed_at, now()),
    updated_at = now()
WHERE EXISTS (
    SELECT 1
    FROM affected_runs AS affected
    WHERE affected.team_id = run.team_id
      AND affected.placement_run_id = run.placement_run_id
);

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
    RAISE EXCEPTION '20260821160001 is irreversible: restart and resubmit unfinished Remember submissions';
END;
$dense_mem_irreversible_remember_normalizer$;
-- +goose StatementEnd
