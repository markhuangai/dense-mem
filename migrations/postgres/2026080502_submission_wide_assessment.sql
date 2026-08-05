-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - The active-run preflight intentionally blocks deployment while an older
--   per-item worker could still create partial semantic state. Drain or
--   terminalize those runs before this migration.
-- - ALTER COLUMN DROP NOT NULL and additive columns do not rewrite retained
--   assessment payloads; new constraints and foreign keys validate existing
--   rows while this transactional migration holds their required locks.
-- - The predicate-registration event is append-only audit provenance. Its RLS
--   policy follows owner-scoped placement records.
-- - Down is valid only before submission-scoped assessments or registration
--   events exist; it deliberately refuses to erase their provenance.
SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $dense_mem_submission_assessment_preflight$
DECLARE
    unfinished_count BIGINT;
BEGIN
    SELECT count(*)
    INTO unfinished_count
    FROM placement_runs
    WHERE status IN ('queued', 'guarded', 'processing');

    IF unfinished_count > 0 THEN
        RAISE EXCEPTION
            'cannot apply 2026080502: % unfinished placement runs must be drained or terminalized before submission-wide assessment cutover',
            unfinished_count;
    END IF;
END
$dense_mem_submission_assessment_preflight$;

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
BEGIN
    SELECT count(*)
    INTO submission_count
    FROM placement_assessments
    WHERE assessment_scope = 'submission';

    SELECT count(*)
    INTO registration_count
    FROM predicate_registration_events;

    IF submission_count > 0 OR registration_count > 0 THEN
        RAISE EXCEPTION
            'cannot roll back 2026080502: submission assessments (%) or predicate registration events (%) exist',
            submission_count,
            registration_count;
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
