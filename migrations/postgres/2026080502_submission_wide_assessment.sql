-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - This exclusive-restart cutover locks the legacy placement work tables,
--   requeues only complete submissions without semantic output, and fails
--   unresolved partial work without changing accepted semantic provenance.
-- - The set-based reconciliation writes placement/review rows and append-only
--   audit outcomes. Down refuses once reconciliation occurred because the
--   prior coordination and review state cannot be reconstructed safely.
-- - ALTER COLUMN DROP NOT NULL and additive columns do not rewrite retained
--   assessment payloads; new constraints and foreign keys validate existing
--   rows while this transactional migration holds their required locks.
-- - The predicate-registration event is append-only audit provenance. Its RLS
--   policy follows owner-scoped placement records.
-- - Down is valid only before reconciliation, submission-scoped assessments,
--   or registration events exist; it deliberately refuses to erase provenance.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

LOCK TABLE
    placement_runs,
    placement_items,
    evidence_fragments,
    evidence_security_events,
    placement_assessments,
    review_tasks,
    entity_resolution_events,
    relationship_observations,
    placement_outcomes
IN SHARE ROW EXCLUSIVE MODE;

CREATE TEMP TABLE dense_mem_2026080502_legacy_runs
ON COMMIT DROP
AS
WITH item_shape AS (
    SELECT item.team_id,
           item.placement_run_id,
           count(*) AS item_count,
           bool_and(
               item.status IN ('queued', 'processing')
               AND item.category = 'pending'
               AND item.evidence_index = fragment.evidence_index
           ) AS clean_items
    FROM placement_items AS item
    JOIN evidence_fragments AS fragment
      ON fragment.team_id = item.team_id
     AND fragment.fragment_id = item.fragment_id
     AND fragment.ingest_id = item.ingest_id
     AND fragment.owner_profile_id = item.owner_profile_id
    GROUP BY item.team_id, item.placement_run_id
), fragment_shape AS (
    SELECT team_id, ingest_id, count(*) AS fragment_count
    FROM evidence_fragments
    GROUP BY team_id, ingest_id
), reviewed_runs AS (
    SELECT DISTINCT item.team_id, item.placement_run_id
    FROM review_tasks AS task
    JOIN placement_items AS item
      ON item.team_id = task.team_id
     AND item.placement_item_id = task.placement_item_id
    UNION
    SELECT DISTINCT run.team_id, run.placement_run_id
    FROM review_tasks AS task
    JOIN placement_runs AS run
      ON run.team_id = task.team_id
     AND run.ingest_id = task.ingest_id
), resolved_runs AS (
    SELECT DISTINCT run.team_id, run.placement_run_id
    FROM entity_resolution_events AS event
    JOIN placement_runs AS run
      ON run.team_id = event.team_id
     AND run.ingest_id = event.ingest_id
), observed_runs AS (
    SELECT DISTINCT run.team_id, run.placement_run_id
    FROM relationship_observations AS observation
    JOIN placement_runs AS run
      ON run.team_id = observation.team_id
     AND run.ingest_id = observation.ingest_id
)
SELECT run.team_id,
       run.placement_run_id,
       run.ingest_id,
       run.owner_profile_id,
       run.status AS previous_status,
       run.attempts AS previous_attempts,
       transaction_timestamp() AS migration_at,
       CASE
           WHEN run.status IN ('queued', 'guarded', 'processing')
            AND COALESCE(item_shape.item_count, 0) > 0
            AND item_shape.item_count = COALESCE(fragment_shape.fragment_count, 0)
            AND item_shape.clean_items
            AND reviewed_runs.placement_run_id IS NULL
            AND resolved_runs.placement_run_id IS NULL
            AND observed_runs.placement_run_id IS NULL
           THEN 'requeue'
           ELSE 'close'
       END AS cutover_action
FROM placement_runs AS run
LEFT JOIN item_shape
  ON item_shape.team_id = run.team_id
 AND item_shape.placement_run_id = run.placement_run_id
LEFT JOIN fragment_shape
  ON fragment_shape.team_id = run.team_id
 AND fragment_shape.ingest_id = run.ingest_id
LEFT JOIN reviewed_runs
  ON reviewed_runs.team_id = run.team_id
 AND reviewed_runs.placement_run_id = run.placement_run_id
LEFT JOIN resolved_runs
  ON resolved_runs.team_id = run.team_id
 AND resolved_runs.placement_run_id = run.placement_run_id
LEFT JOIN observed_runs
  ON observed_runs.team_id = run.team_id
 AND observed_runs.placement_run_id = run.placement_run_id
WHERE run.status IN ('queued', 'guarded', 'processing', 'awaiting_review');

ALTER TABLE dense_mem_2026080502_legacy_runs
    ADD PRIMARY KEY (team_id, placement_run_id);

CREATE TEMP TABLE dense_mem_2026080502_closed_items
ON COMMIT DROP
AS
SELECT item.team_id,
       item.placement_run_id,
       item.placement_item_id,
       item.owner_profile_id,
       item.status AS previous_status,
       item.category AS previous_category,
       legacy.migration_at
FROM placement_items AS item
JOIN dense_mem_2026080502_legacy_runs AS legacy
  ON legacy.team_id = item.team_id
 AND legacy.placement_run_id = item.placement_run_id
 AND legacy.cutover_action = 'close'
WHERE item.status IN ('queued', 'processing', 'awaiting_review');

ALTER TABLE dense_mem_2026080502_closed_items
    ADD PRIMARY KEY (team_id, placement_item_id);

CREATE TEMP TABLE dense_mem_2026080502_closed_tasks
ON COMMIT DROP
AS
SELECT task.team_id,
       task.review_task_id,
       legacy.migration_at
FROM review_tasks AS task
JOIN placement_items AS item
  ON item.team_id = task.team_id
 AND item.placement_item_id = task.placement_item_id
JOIN dense_mem_2026080502_legacy_runs AS legacy
  ON legacy.team_id = item.team_id
 AND legacy.placement_run_id = item.placement_run_id
 AND legacy.cutover_action = 'close'
WHERE task.status IN ('open', 'acknowledged');

ALTER TABLE dense_mem_2026080502_closed_tasks
    ADD PRIMARY KEY (team_id, review_task_id);

INSERT INTO placement_outcomes (
    team_id, placement_run_id, owner_profile_id,
    outcome_kind, status, idempotency_key, payload
)
SELECT legacy.team_id,
       legacy.placement_run_id,
       legacy.owner_profile_id,
       'submission_cutover_requeued',
       'requeued',
       'migration:2026080502:requeue:' || legacy.placement_run_id::text,
       jsonb_build_object(
           'migration_version', '2026080502',
           'previous_status', legacy.previous_status,
           'previous_attempts', legacy.previous_attempts,
           'target_assessment_scope', 'submission',
           'semantic_commit_performed', false
       )
FROM dense_mem_2026080502_legacy_runs AS legacy
WHERE legacy.cutover_action = 'requeue'
ON CONFLICT (team_id, owner_profile_id, idempotency_key)
WHERE idempotency_key <> ''
DO NOTHING;

UPDATE placement_items AS item
SET status = 'queued',
    version = item.version + 1,
    updated_at = legacy.migration_at
FROM dense_mem_2026080502_legacy_runs AS legacy
WHERE legacy.cutover_action = 'requeue'
  AND item.team_id = legacy.team_id
  AND item.placement_run_id = legacy.placement_run_id
  AND item.status = 'processing';

UPDATE placement_runs AS run
SET status = CASE
        WHEN EXISTS (
            SELECT 1
            FROM placement_items AS item
            JOIN evidence_security_events AS event
              ON event.team_id = item.team_id
             AND event.fragment_id = item.fragment_id
             AND event.owner_profile_id = item.owner_profile_id
            WHERE item.team_id = run.team_id
              AND item.placement_run_id = run.placement_run_id
              AND event.decision = 'guarded'
        ) THEN 'guarded'
        ELSE 'queued'
    END,
    attempts = 0,
    available_at = GREATEST(run.available_at, legacy.migration_at),
    lease_until = NULL,
    worker_id = '',
    error = '',
    completed_at = NULL,
    updated_at = legacy.migration_at
FROM dense_mem_2026080502_legacy_runs AS legacy
WHERE legacy.cutover_action = 'requeue'
  AND run.team_id = legacy.team_id
  AND run.placement_run_id = legacy.placement_run_id;

UPDATE review_tasks AS task
SET status = 'canceled',
    resolution = task.resolution || jsonb_build_object(
        'actor', 'system',
        'action', 'canceled',
        'reason', 'legacy_submission_cutover',
        'migration_version', '2026080502'
    ),
    resolved_at = COALESCE(task.resolved_at, closed.migration_at),
    updated_at = closed.migration_at,
    version = task.version + 1
FROM dense_mem_2026080502_closed_tasks AS closed
WHERE task.team_id = closed.team_id
  AND task.review_task_id = closed.review_task_id;

INSERT INTO placement_outcomes (
    team_id, placement_run_id, placement_item_id, owner_profile_id,
    outcome_kind, status, idempotency_key, payload
)
SELECT closed.team_id,
       closed.placement_run_id,
       closed.placement_item_id,
       closed.owner_profile_id,
       'submission_cutover_closed',
       'failed',
       'migration:2026080502:close-item:' || closed.placement_item_id::text,
       jsonb_build_object(
           'migration_version', '2026080502',
           'reason', 'legacy_submission_not_convertible',
           'previous_status', closed.previous_status,
           'previous_category', closed.previous_category,
           'prior_semantic_output_unchanged', true,
           'semantic_commit_performed', false
       )
FROM dense_mem_2026080502_closed_items AS closed
ON CONFLICT (team_id, owner_profile_id, idempotency_key)
WHERE idempotency_key <> ''
DO NOTHING;

UPDATE placement_items AS item
SET status = 'failed',
    category = 'failed',
    error = 'legacy per-item placement closed by submission-wide cutover',
    result = item.result || jsonb_build_object(
        'legacy_submission_cutover', jsonb_build_object(
            'migration_version', '2026080502',
            'reason', 'legacy_submission_not_convertible',
            'status', 'failed'
        )
    ),
    version = item.version + 1,
    updated_at = closed.migration_at
FROM dense_mem_2026080502_closed_items AS closed
WHERE item.team_id = closed.team_id
  AND item.placement_item_id = closed.placement_item_id;

INSERT INTO placement_outcomes (
    team_id, placement_run_id, owner_profile_id,
    outcome_kind, status, idempotency_key, payload
)
SELECT legacy.team_id,
       legacy.placement_run_id,
       legacy.owner_profile_id,
       'submission_cutover_closed',
       'failed',
       'migration:2026080502:close-run:' || legacy.placement_run_id::text,
       jsonb_build_object(
           'migration_version', '2026080502',
           'reason', 'legacy_submission_not_convertible',
           'previous_status', legacy.previous_status,
           'previous_attempts', legacy.previous_attempts,
           'prior_semantic_output_unchanged', true,
           'semantic_commit_performed', false
       )
FROM dense_mem_2026080502_legacy_runs AS legacy
WHERE legacy.cutover_action = 'close'
ON CONFLICT (team_id, owner_profile_id, idempotency_key)
WHERE idempotency_key <> ''
DO NOTHING;

UPDATE placement_runs AS run
SET status = 'failed',
    worker_id = '',
    lease_until = NULL,
    error = 'legacy per-item placement closed by submission-wide cutover',
    completed_at = legacy.migration_at,
    updated_at = legacy.migration_at
FROM dense_mem_2026080502_legacy_runs AS legacy
WHERE legacy.cutover_action = 'close'
  AND run.team_id = legacy.team_id
  AND run.placement_run_id = legacy.placement_run_id;

INSERT INTO placement_outcomes (
    team_id, placement_run_id, owner_profile_id,
    outcome_kind, status, idempotency_key, payload
)
SELECT legacy.team_id,
       legacy.placement_run_id,
       legacy.owner_profile_id,
       'telemetry_first_disposition',
       'failed',
       'telemetry:first_disposition:' || legacy.placement_run_id::text,
       jsonb_build_object('telemetry', 'first_disposition')
FROM dense_mem_2026080502_legacy_runs AS legacy
WHERE legacy.cutover_action = 'close'
  AND NOT EXISTS (
      SELECT 1
      FROM placement_outcomes AS existing
      WHERE existing.team_id = legacy.team_id
        AND existing.placement_run_id = legacy.placement_run_id
        AND existing.outcome_kind = 'telemetry_first_disposition'
  )
ON CONFLICT DO NOTHING;

DO $dense_mem_submission_assessment_reconciliation_check$
DECLARE
    incompatible_count BIGINT;
BEGIN
    SELECT count(*)
    INTO incompatible_count
    FROM placement_runs AS run
    WHERE run.status IN ('processing', 'awaiting_review')
       OR (
           run.status IN ('queued', 'guarded')
           AND (
               run.worker_id <> ''
               OR run.lease_until IS NOT NULL
               OR run.attempts <> 0
               OR NOT EXISTS (
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
                     AND (item.status <> 'queued' OR item.category <> 'pending')
               )
               OR (
                   SELECT count(*)
                   FROM placement_items AS item
                   WHERE item.team_id = run.team_id
                     AND item.placement_run_id = run.placement_run_id
               ) <> (
                   SELECT count(*)
                   FROM evidence_fragments AS fragment
                   WHERE fragment.team_id = run.team_id
                     AND fragment.ingest_id = run.ingest_id
               )
               OR EXISTS (
                   SELECT 1
                   FROM review_tasks AS task
                   WHERE task.team_id = run.team_id
                     AND (
                         task.ingest_id = run.ingest_id
                         OR EXISTS (
                             SELECT 1
                             FROM placement_items AS item
                             WHERE item.team_id = run.team_id
                               AND item.placement_run_id = run.placement_run_id
                               AND item.placement_item_id = task.placement_item_id
                         )
                     )
               )
               OR EXISTS (
                   SELECT 1
                   FROM entity_resolution_events AS event
                   WHERE event.team_id = run.team_id
                     AND event.ingest_id = run.ingest_id
               )
               OR EXISTS (
                   SELECT 1
                   FROM relationship_observations AS observation
                   WHERE observation.team_id = run.team_id
                     AND observation.ingest_id = run.ingest_id
               )
           )
       );

    IF incompatible_count > 0 THEN
        RAISE EXCEPTION
            'cannot apply 2026080502: % incompatible placement runs remain after legacy reconciliation',
            incompatible_count;
    END IF;
END
$dense_mem_submission_assessment_reconciliation_check$;

ALTER TABLE placement_runs
    ADD COLUMN IF NOT EXISTS assessor_attempt_id UUID NULL,
    ADD COLUMN IF NOT EXISTS assessor_attempted_at TIMESTAMPTZ NULL;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_assessor_attempt_pair_check;
ALTER TABLE placement_runs
    ADD CONSTRAINT placement_runs_assessor_attempt_pair_check CHECK (
        (assessor_attempt_id IS NULL) = (assessor_attempted_at IS NULL)
    );

ALTER TABLE placement_assessments
    ADD COLUMN IF NOT EXISTS assessment_scope TEXT NOT NULL DEFAULT 'item',
    ADD COLUMN IF NOT EXISTS placement_run_id UUID NULL,
    ADD COLUMN IF NOT EXISTS ingest_id UUID NULL;

ALTER TABLE placement_assessments
    ALTER COLUMN placement_item_id DROP NOT NULL,
    ALTER COLUMN claim_key DROP NOT NULL;

ALTER TABLE placement_assessments
    DROP CONSTRAINT IF EXISTS placement_assessments_scope_shape_check;
ALTER TABLE placement_assessments
    ADD CONSTRAINT placement_assessments_scope_shape_check CHECK (
        (
            assessment_scope = 'item'
            AND placement_item_id IS NOT NULL
            AND claim_key IS NOT NULL
            AND placement_run_id IS NULL
            AND ingest_id IS NULL
        )
        OR (
            assessment_scope = 'submission'
            AND placement_item_id IS NULL
            AND claim_key IS NULL
            AND placement_run_id IS NOT NULL
            AND ingest_id IS NOT NULL
        )
    );

ALTER TABLE placement_assessments
    DROP CONSTRAINT IF EXISTS placement_assessments_submission_run_ref;
ALTER TABLE placement_assessments
    ADD CONSTRAINT placement_assessments_submission_run_ref
    FOREIGN KEY (team_id, placement_run_id, ingest_id, owner_profile_id)
    REFERENCES placement_runs(team_id, placement_run_id, ingest_id, owner_profile_id)
    ON DELETE RESTRICT;

CREATE UNIQUE INDEX IF NOT EXISTS placement_assessments_submission_run_unique
    ON placement_assessments(team_id, placement_run_id)
    WHERE assessment_scope = 'submission';

CREATE INDEX IF NOT EXISTS placement_assessments_submission_owner_created_idx
    ON placement_assessments(team_id, owner_profile_id, created_at ASC, placement_run_id)
    WHERE assessment_scope = 'submission';

CREATE TABLE IF NOT EXISTS predicate_registration_events (
    team_id UUID NOT NULL,
    predicate_registration_event_id UUID NOT NULL DEFAULT gen_random_uuid(),
    placement_run_id UUID NOT NULL,
    assessment_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    relationship_ref TEXT NOT NULL,
    registration_action TEXT NOT NULL,
    predicate_key TEXT NOT NULL,
    predicate_version INTEGER NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, predicate_registration_event_id),
    UNIQUE (team_id, placement_run_id, relationship_ref),
    FOREIGN KEY (team_id, placement_run_id) REFERENCES placement_runs(team_id, placement_run_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, assessment_id) REFERENCES placement_assessments(team_id, assessment_id) ON DELETE RESTRICT,
    FOREIGN KEY (team_id, placement_run_id, owner_profile_id)
        REFERENCES placement_runs(team_id, placement_run_id, owner_profile_id)
        ON DELETE RESTRICT,
    CONSTRAINT predicate_registration_events_ref_nonempty CHECK (btrim(relationship_ref) <> ''),
    CONSTRAINT predicate_registration_events_action_check CHECK (registration_action IN ('created', 'reused')),
    CONSTRAINT predicate_registration_events_key_nonempty CHECK (btrim(predicate_key) <> ''),
    CONSTRAINT predicate_registration_events_version_check CHECK (predicate_version >= 1),
    CONSTRAINT predicate_registration_events_metadata_object_check CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS predicate_registration_events_run_created_idx
    ON predicate_registration_events(team_id, placement_run_id, created_at ASC, predicate_registration_event_id);

DROP TRIGGER IF EXISTS predicate_registration_events_append_only ON predicate_registration_events;
CREATE TRIGGER predicate_registration_events_append_only
    BEFORE UPDATE OR DELETE ON predicate_registration_events
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE predicate_registration_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE predicate_registration_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS predicate_registration_events_select ON predicate_registration_events;
CREATE POLICY predicate_registration_events_select ON predicate_registration_events
    FOR SELECT USING (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) IN ('team', 'profile')
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
    );

DROP POLICY IF EXISTS predicate_registration_events_insert ON predicate_registration_events;
CREATE POLICY predicate_registration_events_insert ON predicate_registration_events
    FOR INSERT WITH CHECK (
        current_setting('app.tx_mode', true) IN ('system', 'migration')
        OR (
            current_setting('app.tx_mode', true) = 'team'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        )
        OR (
            current_setting('app.tx_mode', true) = 'profile'
            AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
            AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
        )
    );

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $dense_mem_submission_assessment_rollback_preflight$
DECLARE
    submission_count BIGINT;
    registration_count BIGINT;
    reconciliation_count BIGINT;
BEGIN
    SELECT count(*)
    INTO submission_count
    FROM placement_assessments
    WHERE assessment_scope = 'submission';

    SELECT count(*)
    INTO registration_count
    FROM predicate_registration_events;

    SELECT count(*)
    INTO reconciliation_count
    FROM placement_outcomes
    WHERE outcome_kind IN ('submission_cutover_requeued', 'submission_cutover_closed');

    IF submission_count > 0 OR registration_count > 0 OR reconciliation_count > 0 THEN
        RAISE EXCEPTION
            'cannot roll back 2026080502: submission assessments (%), predicate registration events (%), or legacy reconciliation outcomes (%) exist',
            submission_count,
            registration_count,
            reconciliation_count;
    END IF;
END
$dense_mem_submission_assessment_rollback_preflight$;

DROP TABLE IF EXISTS predicate_registration_events;

DROP INDEX IF EXISTS placement_assessments_submission_owner_created_idx;
DROP INDEX IF EXISTS placement_assessments_submission_run_unique;

ALTER TABLE placement_assessments
    DROP CONSTRAINT IF EXISTS placement_assessments_submission_run_ref,
    DROP CONSTRAINT IF EXISTS placement_assessments_scope_shape_check;

ALTER TABLE placement_assessments
    ALTER COLUMN placement_item_id SET NOT NULL,
    ALTER COLUMN claim_key SET NOT NULL,
    DROP COLUMN IF EXISTS ingest_id,
    DROP COLUMN IF EXISTS placement_run_id,
    DROP COLUMN IF EXISTS assessment_scope;

ALTER TABLE placement_runs
    DROP CONSTRAINT IF EXISTS placement_runs_assessor_attempt_pair_check,
    DROP COLUMN IF EXISTS assessor_attempted_at,
    DROP COLUMN IF EXISTS assessor_attempt_id;

-- +goose StatementEnd
