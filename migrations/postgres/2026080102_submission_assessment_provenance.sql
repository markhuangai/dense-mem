-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - Each nullable-column addition and foreign-key validation takes a brief
--   table lock but does not rewrite existing event rows.
-- - Existing placement assessment provenance remains in assessment_id.
--   New submission provenance uses a separate, mutually exclusive reference.
-- - Rollback is safe only while no semantic event references a submission
--   assessment; otherwise removing that provenance would lose audit lineage.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE entity_resolution_events
    ADD COLUMN IF NOT EXISTS submission_assessment_id UUID NULL;
ALTER TABLE verification_events
    ADD COLUMN IF NOT EXISTS submission_assessment_id UUID NULL;

ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_submission_assessment_ref;
ALTER TABLE entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_submission_assessment_ref
    FOREIGN KEY (team_id, submission_assessment_id)
    REFERENCES submission_assessments(team_id, assessment_id)
    ON DELETE RESTRICT;

ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_submission_assessment_ref;
ALTER TABLE verification_events
    ADD CONSTRAINT verification_events_submission_assessment_ref
    FOREIGN KEY (team_id, submission_assessment_id)
    REFERENCES submission_assessments(team_id, assessment_id)
    ON DELETE RESTRICT;

ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_assessment_source_check;
ALTER TABLE entity_resolution_events
    ADD CONSTRAINT entity_resolution_events_assessment_source_check
    CHECK (assessment_id IS NULL OR submission_assessment_id IS NULL) NOT VALID;
ALTER TABLE entity_resolution_events
    VALIDATE CONSTRAINT entity_resolution_events_assessment_source_check;

ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_assessment_source_check;
ALTER TABLE verification_events
    ADD CONSTRAINT verification_events_assessment_source_check
    CHECK (assessment_id IS NULL OR submission_assessment_id IS NULL) NOT VALID;
ALTER TABLE verification_events
    VALIDATE CONSTRAINT verification_events_assessment_source_check;

CREATE INDEX IF NOT EXISTS entity_resolution_events_submission_assessment_idx
    ON entity_resolution_events(team_id, submission_assessment_id)
    WHERE submission_assessment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS verification_events_submission_assessment_idx
    ON verification_events(team_id, submission_assessment_id)
    WHERE submission_assessment_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM entity_resolution_events
        WHERE submission_assessment_id IS NOT NULL
    ) OR EXISTS (
        SELECT 1
        FROM verification_events
        WHERE submission_assessment_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'cannot roll back 2026080102: submission assessment provenance exists';
    END IF;
END $$;

DROP INDEX IF EXISTS verification_events_submission_assessment_idx;
DROP INDEX IF EXISTS entity_resolution_events_submission_assessment_idx;

ALTER TABLE verification_events
    DROP CONSTRAINT IF EXISTS verification_events_assessment_source_check,
    DROP CONSTRAINT IF EXISTS verification_events_submission_assessment_ref,
    DROP COLUMN IF EXISTS submission_assessment_id;
ALTER TABLE entity_resolution_events
    DROP CONSTRAINT IF EXISTS entity_resolution_events_assessment_source_check,
    DROP CONSTRAINT IF EXISTS entity_resolution_events_submission_assessment_ref,
    DROP COLUMN IF EXISTS submission_assessment_id;

-- +goose StatementEnd
