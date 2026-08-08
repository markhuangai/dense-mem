-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - Both tables are new and do not rewrite existing semantic history.
-- - Foreign keys take short catalog locks while validating against existing
--   profile and Relationship keys.
-- - RLS preserves team-wide read visibility while restricting workflow
--   mutation to the Relationship owner profile.
-- - Rollback is blocked after the first correction submission because its
--   workflow and accepted correction history must not be discarded.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

CREATE TABLE relationship_correction_submissions (
    team_id UUID NOT NULL,
    submission_id UUID NOT NULL DEFAULT gen_random_uuid(),
    owner_profile_id UUID NOT NULL,
    relationship_id UUID NOT NULL,
    expected_version INTEGER NOT NULL,
    request_hash TEXT NOT NULL,
    patch JSONB NOT NULL DEFAULT '{}'::jsonb,
    supports JSONB NOT NULL DEFAULT '[]'::jsonb,
    reason TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    confirmation_idempotency_key TEXT NOT NULL DEFAULT '',
    confirmation_request_hash TEXT NOT NULL DEFAULT '',
    processing_state TEXT NOT NULL,
    confirmation_round INTEGER NOT NULL DEFAULT 0,
    confirmation_token TEXT NOT NULL DEFAULT '',
    confirmation_expires_at TIMESTAMPTZ NULL,
    candidates JSONB NOT NULL DEFAULT '[]'::jsonb,
    selection JSONB NOT NULL DEFAULT '{}'::jsonb,
    successor_relationship_id UUID NULL,
    reused_successor BOOLEAN NOT NULL DEFAULT false,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NULL,
    PRIMARY KEY (team_id, submission_id),
    CONSTRAINT relationship_correction_submissions_owner_ref_unique
        UNIQUE (team_id, submission_id, owner_profile_id),
    CONSTRAINT relationship_correction_submissions_owner_fk
        FOREIGN KEY (team_id, owner_profile_id)
        REFERENCES semantic_profile_refs(team_id, profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_correction_submissions_target_fk
        FOREIGN KEY (team_id, relationship_id, owner_profile_id)
        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_correction_submissions_successor_fk
        FOREIGN KEY (team_id, successor_relationship_id, owner_profile_id)
        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_correction_submissions_expected_version_check
        CHECK (expected_version >= 1),
    CONSTRAINT relationship_correction_submissions_request_hash_check
        CHECK (btrim(request_hash) <> ''),
    CONSTRAINT relationship_correction_submissions_patch_check
        CHECK (jsonb_typeof(patch) = 'object'),
    CONSTRAINT relationship_correction_submissions_supports_check
        CHECK (jsonb_typeof(supports) = 'array'),
    CONSTRAINT relationship_correction_submissions_reason_check
        CHECK (btrim(reason) <> '' AND char_length(reason) <= 1000),
    CONSTRAINT relationship_correction_submissions_idempotency_check
        CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT relationship_correction_submissions_confirmation_idempotency_check
        CHECK (
            (confirmation_idempotency_key = '' AND confirmation_request_hash = '')
            OR (btrim(confirmation_idempotency_key) <> '' AND btrim(confirmation_request_hash) <> '')
        ),
    CONSTRAINT relationship_correction_submissions_state_check
        CHECK (processing_state IN ('processing', 'awaiting_confirmation', 'completed', 'rejected', 'failed')),
    CONSTRAINT relationship_correction_submissions_confirmation_round_check
        CHECK (confirmation_round BETWEEN 0 AND 1),
    CONSTRAINT relationship_correction_submissions_candidates_check
        CHECK (jsonb_typeof(candidates) = 'array'),
    CONSTRAINT relationship_correction_submissions_selection_check
        CHECK (jsonb_typeof(selection) = 'object'),
    CONSTRAINT relationship_correction_submissions_confirmation_state_check
        CHECK (
            (processing_state = 'awaiting_confirmation'
             AND confirmation_round = 0
             AND btrim(confirmation_token) <> ''
             AND confirmation_expires_at IS NOT NULL
             AND jsonb_array_length(candidates) > 1)
            OR processing_state <> 'awaiting_confirmation'
        ),
    CONSTRAINT relationship_correction_submissions_terminal_state_check
        CHECK (
            (processing_state IN ('completed', 'rejected', 'failed') AND completed_at IS NOT NULL)
            OR processing_state IN ('processing', 'awaiting_confirmation')
        ),
    CONSTRAINT relationship_correction_submissions_result_check
        CHECK (
            (processing_state = 'completed' AND successor_relationship_id IS NOT NULL)
            OR processing_state <> 'completed'
        )
);

CREATE UNIQUE INDEX relationship_correction_submissions_idempotency_unique
    ON relationship_correction_submissions(team_id, owner_profile_id, idempotency_key);
CREATE UNIQUE INDEX relationship_correction_submissions_confirmation_idempotency_unique
    ON relationship_correction_submissions(team_id, owner_profile_id, confirmation_idempotency_key)
    WHERE confirmation_idempotency_key <> '';
CREATE INDEX relationship_correction_submissions_owner_created_idx
    ON relationship_correction_submissions(team_id, owner_profile_id, created_at DESC, submission_id DESC);

CREATE TABLE relationship_correction_events (
    team_id UUID NOT NULL,
    correction_id UUID NOT NULL DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL,
    owner_profile_id UUID NOT NULL,
    original_relationship_id UUID NOT NULL,
    original_relationship_version INTEGER NOT NULL,
    successor_relationship_id UUID NOT NULL,
    successor_relationship_version INTEGER NOT NULL,
    reused_successor BOOLEAN NOT NULL DEFAULT false,
    patch JSONB NOT NULL DEFAULT '{}'::jsonb,
    supports JSONB NOT NULL DEFAULT '[]'::jsonb,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, correction_id),
    CONSTRAINT relationship_correction_events_submission_unique
        UNIQUE (team_id, submission_id),
    CONSTRAINT relationship_correction_events_submission_fk
        FOREIGN KEY (team_id, submission_id, owner_profile_id)
        REFERENCES relationship_correction_submissions(team_id, submission_id, owner_profile_id)
        ON DELETE RESTRICT,
    CONSTRAINT relationship_correction_events_original_fk
        FOREIGN KEY (team_id, original_relationship_id, owner_profile_id)
        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_correction_events_successor_fk
        FOREIGN KEY (team_id, successor_relationship_id, owner_profile_id)
        REFERENCES relationship_records(team_id, relationship_id, owner_profile_id) ON DELETE RESTRICT,
    CONSTRAINT relationship_correction_events_version_check
        CHECK (original_relationship_version >= 1 AND successor_relationship_version >= 1),
    CONSTRAINT relationship_correction_events_patch_check
        CHECK (jsonb_typeof(patch) = 'object'),
    CONSTRAINT relationship_correction_events_supports_check
        CHECK (jsonb_typeof(supports) = 'array'),
    CONSTRAINT relationship_correction_events_reason_check
        CHECK (btrim(reason) <> '' AND char_length(reason) <= 1000)
);

DROP TRIGGER IF EXISTS relationship_correction_events_append_only ON relationship_correction_events;
CREATE TRIGGER relationship_correction_events_append_only
    BEFORE UPDATE OR DELETE ON relationship_correction_events
    FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

ALTER TABLE relationship_correction_submissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_correction_submissions FORCE ROW LEVEL SECURITY;
ALTER TABLE relationship_correction_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE relationship_correction_events FORCE ROW LEVEL SECURITY;

CREATE POLICY relationship_correction_submissions_select ON relationship_correction_submissions
FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);
CREATE POLICY relationship_correction_submissions_insert ON relationship_correction_submissions
FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);
CREATE POLICY relationship_correction_submissions_update ON relationship_correction_submissions
FOR UPDATE USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
) WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) = 'profile'
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
        AND owner_profile_id = nullif(current_setting('app.current_profile_id', true), '')::uuid
    )
);

CREATE POLICY relationship_correction_events_select ON relationship_correction_events
FOR SELECT USING (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
    OR (
        current_setting('app.tx_mode', true) IN ('team', 'profile')
        AND team_id = nullif(current_setting('app.current_team_id', true), '')::uuid
    )
);
CREATE POLICY relationship_correction_events_insert ON relationship_correction_events
FOR INSERT WITH CHECK (
    current_setting('app.tx_mode', true) IN ('system', 'migration')
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

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM relationship_correction_submissions)
       OR EXISTS (SELECT 1 FROM relationship_correction_events) THEN
        RAISE EXCEPTION 'cannot roll back 2026080801: relationship correction history exists';
    END IF;
END $$;

DROP TABLE relationship_correction_events;
DROP TABLE relationship_correction_submissions;

-- +goose StatementEnd
