-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin

-- Issue #317 adds only durable foundations. Retry/replay activation remains
-- owned by #318. Existing attempts are append-only and retain NULL retryability
-- so the legacy effective semantics can be derived without rewriting history.
-- Lock/rewrite impact: additive columns, constraints, and policies use short DDL
-- locks; validation fails closed if deployed data is out of bounds, and the
-- retryability index is built concurrently outside a transaction.
-- RLS impact: held artifact reads remain system/control-only; retention updates
-- are allowed only through a lock-scoped system transaction.
-- Backfill: active legal holds reconcile their existing Remember artifacts in
-- the scoped system transaction below; retryability remains NULL and is
-- projected by outcome for legacy attempts.
-- Backward compatibility: #317 is dormant; public v2.6.1 Remember execution is unchanged.
-- Rollback: once a new attempt or held artifact exists, the additive history is
-- irreversible; restore a verified snapshot rather than rewriting it.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.private_erasure_space_id', '', true);
SELECT set_config('app.remember_failure_artifact_purge', '', true);
SELECT set_config('app.remember_failure_artifact_retention_space_id', '', true);
SELECT set_config('app.remember_failure_artifact_retention_value', '', true);
SELECT set_config('lock_timeout', '30s', true);

ALTER TABLE remember_attempts
    ADD COLUMN IF NOT EXISTS retryable BOOLEAN NULL;
ALTER TABLE remember_attempts
    DROP CONSTRAINT IF EXISTS remember_attempts_retryable_outcome_check;
ALTER TABLE remember_attempts
    ADD CONSTRAINT remember_attempts_retryable_outcome_check
    CHECK (retryable IS NULL OR retryable = false OR (retryable = true AND outcome = 'failed')) NOT VALID;

-- +goose StatementEnd

-- Keep constraint validation in its own statement so its table scan does not
-- share a transaction with unrelated migration work.
-- +goose StatementBegin
SELECT set_config('lock_timeout', '30s', true);
ALTER TABLE remember_attempts
    VALIDATE CONSTRAINT remember_attempts_retryable_outcome_check;
-- +goose StatementEnd

-- CREATE INDEX CONCURRENTLY may leave an invalid catalog entry after an
-- interrupted build. Remove any prior temporary entry, rename an invalid
-- canonical entry, and rebuild it on retry.
DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_failed_retryable_idx_invalid;

-- +goose StatementBegin
DO $dense_mem_remember_attempts_failed_retryable_invalid_index$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS state
        JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname = 'remember_attempts_failed_retryable_idx'
          AND state.indisvalid IS FALSE
    LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I', candidate.relname, candidate.relname || '_invalid');
    END LOOP;
END
$dense_mem_remember_attempts_failed_retryable_invalid_index$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS remember_attempts_failed_retryable_idx
    ON remember_attempts(team_id, owner_profile_id, idempotency_key, created_at DESC, attempt_id DESC)
    WHERE outcome = 'failed';

DROP INDEX CONCURRENTLY IF EXISTS remember_attempts_failed_retryable_idx_invalid;

-- +goose StatementBegin
ALTER TABLE remember_failure_artifacts
    ADD COLUMN IF NOT EXISTS retained_by_legal_hold BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE remember_failure_artifacts
    DROP CONSTRAINT IF EXISTS remember_failure_artifacts_retention_size_check;
ALTER TABLE remember_failure_artifacts
    ADD CONSTRAINT remember_failure_artifacts_retention_size_check
    CHECK (
        (
            (artifact_kind = 'legacy_submission_quarantine_payload' AND byte_count <= 1048576)
            OR (artifact_kind <> 'legacy_submission_quarantine_payload' AND byte_count <= 262144)
        )
        AND expires_at <= captured_at + interval '7 days'
    ) NOT VALID;

-- +goose StatementEnd

-- +goose StatementBegin
SELECT set_config('lock_timeout', '30s', true);
ALTER TABLE remember_failure_artifacts
    VALIDATE CONSTRAINT remember_failure_artifacts_retention_size_check;

-- +goose StatementEnd

-- +goose StatementBegin
DROP POLICY IF EXISTS remember_failure_artifacts_update ON remember_failure_artifacts;
CREATE POLICY remember_failure_artifacts_update ON remember_failure_artifacts
    FOR UPDATE
    USING (
        current_setting('app.tx_mode', true) = 'system'
        AND NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid IS NOT NULL
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'system'
        AND NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid IS NOT NULL
    );

CREATE OR REPLACE FUNCTION prevent_append_only_mutation()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND TG_TABLE_NAME = 'remember_failure_artifacts' THEN
        IF current_setting('app.tx_mode', true) = 'system'
           AND NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid IS NOT NULL
           AND COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) =
               (current_setting('app.remember_failure_artifact_retention_value', true) = 'true')
           AND (
               to_jsonb(NEW) - ARRAY['retained_by_legal_hold']
           ) = (
               to_jsonb(OLD) - ARRAY['retained_by_legal_hold']
           )
           AND EXISTS (
               SELECT 1
               FROM remember_attempts AS attempt
               WHERE attempt.team_id = NEW.team_id
                 AND attempt.attempt_id = NEW.attempt_id
                 AND attempt.owner_profile_id = NEW.owner_profile_id
                 AND attempt.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
           )
           AND (
               (COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND EXISTS (
                   SELECT 1 FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
               OR
               (NOT COALESCE((to_jsonb(NEW)->>'retained_by_legal_hold')::boolean, false) AND NOT EXISTS (
                   SELECT 1 FROM private_memory_legal_holds AS hold
                   WHERE hold.space_id = NULLIF(current_setting('app.remember_failure_artifact_retention_space_id', true), '')::uuid
                     AND hold.released_at IS NULL
               ))
           ) THEN
            RETURN NEW;
        END IF;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('app.tx_mode', true) = 'system'
       AND (
           NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               = NULLIF(to_jsonb(OLD)->>'space_id', '')::uuid
           OR (
               TG_TABLE_NAME IN ('remember_attempt_events', 'remember_failure_artifacts', 'semantic_assessments')
               AND EXISTS (
                   SELECT 1
                   FROM remember_attempts AS attempt
                   WHERE attempt.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND attempt.attempt_id = NULLIF(to_jsonb(OLD)->>'attempt_id', '')::uuid
                     AND attempt.owner_profile_id = NULLIF(to_jsonb(OLD)->>'owner_profile_id', '')::uuid
                     AND attempt.space_id = NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid
               )
           )
           OR (
               TG_TABLE_NAME = 'remember_failure_artifacts'
               AND current_setting('app.remember_failure_artifact_purge', true) = 'true'
           )
           OR (
               TG_TABLE_NAME = 'relationship_cross_references'
               AND NULLIF(current_setting('app.private_erasure_space_id', true), '')::uuid = (
                   SELECT target.space_id
                   FROM relationship_records AS target
                   WHERE target.team_id = NULLIF(to_jsonb(OLD)->>'team_id', '')::uuid
                     AND target.relationship_id = NULLIF(to_jsonb(OLD)->>'target_relationship_id', '')::uuid
               )
           )
       ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % operations are not allowed', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

SELECT set_config('app.tx_mode', 'system', true);
DO $$
DECLARE
    held_space UUID;
BEGIN
    FOR held_space IN
        SELECT DISTINCT attempt.space_id
        FROM remember_failure_artifacts AS artifact
        JOIN remember_attempts AS attempt
          ON attempt.team_id = artifact.team_id
         AND attempt.attempt_id = artifact.attempt_id
         AND attempt.owner_profile_id = artifact.owner_profile_id
        JOIN private_memory_legal_holds AS hold
          ON hold.space_id = attempt.space_id AND hold.released_at IS NULL
        WHERE attempt.space_id IS NOT NULL
    LOOP
        PERFORM set_config('app.remember_failure_artifact_retention_space_id', held_space::text, true);
        PERFORM set_config('app.remember_failure_artifact_retention_value', 'true', true);
        UPDATE remember_failure_artifacts AS artifact
        SET retained_by_legal_hold = true
        FROM remember_attempts AS attempt
        WHERE attempt.team_id = artifact.team_id
          AND attempt.attempt_id = artifact.attempt_id
          AND attempt.owner_profile_id = artifact.owner_profile_id
          AND attempt.space_id = held_space
          AND NOT artifact.retained_by_legal_hold;
    END LOOP;
END;
$$;

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.remember_failure_artifact_retention_space_id', '', true);
SELECT set_config('app.remember_failure_artifact_retention_value', '', true);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'evidence-first Remember primitives are append-only after deployment; restore a verified snapshot to roll back';
END;
$$;
-- +goose StatementEnd
