-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite analysis:
-- - Provenance columns are nullable/defaulted additions; the follow-on CHECK
--   validations scan submission_runs but do not rewrite staged evidence.
-- - 2026080101 already copied unfinished legacy proposals into secure staging.
--   This repair only normalizes known alias keys. Missing, empty, or non-array
--   proposals become terminal failed audit records rather than provider work.
-- - Accepted evidence and legacy knowledge_ingests remain immutable. Only raw
--   staged input for incompatible terminal submissions is removed.
-- - The legacy repair is irreversible once a legacy-upgrade submission exists.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);

ALTER TABLE submission_runs
    ADD COLUMN IF NOT EXISTS actor_credential_id UUID NULL,
    ADD COLUMN IF NOT EXISTS actor_auth_method TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS actor_role TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS actor_scopes TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT '';

ALTER TABLE submission_runs
    DROP CONSTRAINT IF EXISTS submission_runs_actor_provenance_check;
ALTER TABLE submission_runs
    ADD CONSTRAINT submission_runs_actor_provenance_check CHECK (
        (actor_credential_id IS NULL
            AND actor_auth_method = ''
            AND actor_role = ''
            AND cardinality(actor_scopes) = 0)
        OR (actor_credential_id IS NOT NULL
            AND btrim(actor_auth_method) <> ''
            AND btrim(actor_role) <> '')
    ) NOT VALID;
ALTER TABLE submission_runs
    VALIDATE CONSTRAINT submission_runs_actor_provenance_check;

ALTER TABLE submission_runs
    DROP CONSTRAINT IF EXISTS submission_runs_actor_provenance_bounds_check;
ALTER TABLE submission_runs
    ADD CONSTRAINT submission_runs_actor_provenance_bounds_check CHECK (
        char_length(actor_auth_method) <= 64
        AND char_length(actor_role) <= 64
        AND char_length(correlation_id) <= 512
        AND cardinality(actor_scopes) <= 64
    ) NOT VALID;
ALTER TABLE submission_runs
    VALIDATE CONSTRAINT submission_runs_actor_provenance_bounds_check;

CREATE TEMP TABLE secure_submission_legacy_proposals ON COMMIT DROP AS
SELECT
    submission.team_id,
    submission.submission_id,
    submission.owner_profile_id,
    CASE
        WHEN jsonb_typeof(staged.proposal->'entities') = 'array'
             AND jsonb_array_length(staged.proposal->'entities') > 0
            THEN staged.proposal->'entities'
        WHEN jsonb_typeof(staged.proposal->'entity_hints') = 'array'
             AND jsonb_array_length(staged.proposal->'entity_hints') > 0
            THEN staged.proposal->'entity_hints'
        ELSE NULL
    END AS entities,
    CASE
        WHEN jsonb_typeof(staged.proposal->'relationships') = 'array'
             AND jsonb_array_length(staged.proposal->'relationships') > 0
            THEN staged.proposal->'relationships'
        WHEN jsonb_typeof(staged.proposal->'relationship_hints') = 'array'
             AND jsonb_array_length(staged.proposal->'relationship_hints') > 0
            THEN staged.proposal->'relationship_hints'
        ELSE NULL
    END AS relationships
FROM submission_runs AS submission
JOIN submission_staged_proposals AS staged
  ON staged.team_id = submission.team_id
 AND staged.submission_id = submission.submission_id
JOIN knowledge_ingests AS legacy_ingest
  ON legacy_ingest.team_id = submission.team_id
 AND legacy_ingest.ingest_id = submission.submission_id
WHERE submission.idempotency_key = 'legacy-upgrade:' || submission.submission_id::text
  AND legacy_ingest.error = 'secure_submission_upgrade_staged'
  AND submission.status IN ('queued', 'processing');

CREATE UNIQUE INDEX secure_submission_legacy_proposals_pk
    ON secure_submission_legacy_proposals(team_id, submission_id);

CREATE TEMP TABLE secure_submission_legacy_incompatible ON COMMIT DROP AS
SELECT team_id, submission_id, owner_profile_id
FROM secure_submission_legacy_proposals
WHERE entities IS NULL OR relationships IS NULL;

CREATE UNIQUE INDEX secure_submission_legacy_incompatible_pk
    ON secure_submission_legacy_incompatible(team_id, submission_id);

UPDATE submission_staged_proposals AS staged
SET proposal = jsonb_build_object(
        'entities', legacy.entities,
        'relationships', legacy.relationships
    )
FROM secure_submission_legacy_proposals AS legacy
WHERE staged.team_id = legacy.team_id
  AND staged.submission_id = legacy.submission_id
  AND legacy.entities IS NOT NULL
  AND legacy.relationships IS NOT NULL;

UPDATE submission_runs AS submission
SET status = 'failed',
    lease_until = NULL,
    worker_id = '',
    error_code = 'legacy_proposal_incompatible',
    completed_at = clock_timestamp(),
    updated_at = clock_timestamp()
FROM secure_submission_legacy_incompatible AS legacy
WHERE submission.team_id = legacy.team_id
  AND submission.submission_id = legacy.submission_id;

INSERT INTO submission_outcomes (
    team_id, submission_id, owner_profile_id, outcome_kind,
    status, reason_code, details
)
SELECT legacy.team_id, legacy.submission_id, legacy.owner_profile_id, 'submission',
       'failed', 'legacy_proposal_incompatible',
       jsonb_build_object('search_state', 'not_required')
FROM secure_submission_legacy_incompatible AS legacy;

DELETE FROM submission_staged_evidence AS staged
USING secure_submission_legacy_incompatible AS legacy
WHERE staged.team_id = legacy.team_id
  AND staged.submission_id = legacy.submission_id;

DELETE FROM submission_staged_proposals AS staged
USING secure_submission_legacy_incompatible AS legacy
WHERE staged.team_id = legacy.team_id
  AND staged.submission_id = legacy.submission_id;

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
        FROM submission_runs
        WHERE idempotency_key = 'legacy-upgrade:' || submission_id::text
    ) THEN
        RAISE EXCEPTION 'cannot roll back 2026080103: legacy proposal repair is irreversible';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM submission_runs
        WHERE actor_credential_id IS NOT NULL
           OR actor_auth_method <> ''
           OR actor_role <> ''
           OR cardinality(actor_scopes) <> 0
           OR correlation_id <> ''
    ) THEN
        RAISE EXCEPTION 'cannot roll back 2026080103: authenticated submission provenance exists';
    END IF;
END $$;

ALTER TABLE submission_runs
    DROP CONSTRAINT IF EXISTS submission_runs_actor_provenance_bounds_check,
    DROP CONSTRAINT IF EXISTS submission_runs_actor_provenance_check,
    DROP COLUMN IF EXISTS correlation_id,
    DROP COLUMN IF EXISTS actor_scopes,
    DROP COLUMN IF EXISTS actor_role,
    DROP COLUMN IF EXISTS actor_auth_method,
    DROP COLUMN IF EXISTS actor_credential_id;

-- +goose StatementEnd
