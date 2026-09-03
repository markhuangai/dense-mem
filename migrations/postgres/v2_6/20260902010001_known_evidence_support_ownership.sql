-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: this adds one nullable UUID, backfills it from the
-- existing Relationship owner, then makes it required. The backfill runs in
-- bounded, resumable batches so the append-only support table is not held in a
-- single transaction. The supporting index is built concurrently, and foreign
-- keys are installed NOT VALID before separate validation scans.
-- RLS impact: a temporary UPDATE policy admits only the gated NULL-to-owner
-- backfill through FORCE RLS and is removed before constraint validation.
-- Backward compatibility: existing support rows retain their current owner;
-- new known-evidence rows can point at a different evidence owner while the
-- Relationship and support-decision owner remain unchanged.
-- Rollback: the down migration refuses to discard cross-owner provenance.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SET lock_timeout = '30s';

ALTER TABLE relationship_evidence_supports
    ADD COLUMN IF NOT EXISTS evidence_owner_profile_id UUID NULL;

-- Preserve the legacy direct-insert shape used by migration and UAT seeders;
-- runtime known-evidence writes always provide this value explicitly.
CREATE OR REPLACE FUNCTION dense_mem_relationship_support_evidence_owner_defaults()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.evidence_owner_profile_id IS NULL THEN
        NEW.evidence_owner_profile_id := NEW.owner_profile_id;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS relationship_supports_evidence_owner_defaults ON relationship_evidence_supports;
CREATE TRIGGER relationship_supports_evidence_owner_defaults
    BEFORE INSERT ON relationship_evidence_supports
    FOR EACH ROW
    EXECUTE FUNCTION dense_mem_relationship_support_evidence_owner_defaults();

-- +goose StatementEnd

-- Preserve the existing legal-hold and private-erasure exceptions while adding
-- this migration's narrowly gated NULL-to-relationship-owner backfill. The
-- transaction-local flag is set by the procedure below and cannot authorize
-- ordinary runtime updates.
-- +goose StatementBegin
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
    IF TG_TABLE_NAME = 'relationship_evidence_supports'
       AND TG_OP = 'UPDATE'
       AND current_setting('app.tx_mode', true) = 'migration'
       AND current_setting('app.known_evidence_support_ownership_backfill', true) = 'true'
       AND (to_jsonb(OLD)->>'evidence_owner_profile_id') IS NULL
       AND (to_jsonb(NEW)->>'evidence_owner_profile_id') IS NOT NULL
       AND (to_jsonb(NEW)->>'evidence_owner_profile_id') IS NOT DISTINCT FROM (to_jsonb(OLD)->>'owner_profile_id')
       AND (to_jsonb(NEW) - 'evidence_owner_profile_id') = (to_jsonb(OLD) - 'evidence_owner_profile_id')
    THEN
        RETURN NEW;
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
-- +goose StatementEnd

-- Keep the resumable backfill bounded by the remaining NULL rows instead of
-- rescanning every already-processed support row on each batch.
DROP INDEX CONCURRENTLY IF EXISTS relationship_supports_evidence_owner_backfill_null_idx;
CREATE INDEX CONCURRENTLY relationship_supports_evidence_owner_backfill_null_idx
    ON relationship_evidence_supports(team_id, support_id)
    WHERE evidence_owner_profile_id IS NULL;

-- UPDATE through FORCE RLS needs an explicit policy in addition to the
-- existing migration-mode SELECT policy. Keep the same narrow gate as the
-- append-only trigger, and replace any policy left by an interrupted attempt.
DROP POLICY IF EXISTS relationship_supports_evidence_owner_backfill_update
    ON relationship_evidence_supports;
CREATE POLICY relationship_supports_evidence_owner_backfill_update
    ON relationship_evidence_supports
    FOR UPDATE
    USING (
        current_setting('app.tx_mode', true) = 'migration'
        AND current_setting('app.known_evidence_support_ownership_backfill', true) = 'true'
        AND evidence_owner_profile_id IS NULL
    )
    WITH CHECK (
        current_setting('app.tx_mode', true) = 'migration'
        AND current_setting('app.known_evidence_support_ownership_backfill', true) = 'true'
        AND evidence_owner_profile_id = owner_profile_id
    );

-- The support ledger remains append-only at runtime. Each batch enables the
-- transaction-local backfill gate, then commits before the next batch is
-- selected. A failed batch rolls back its row work; rerunning the migration
-- continues from the remaining NULL values.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE dense_mem_backfill_known_evidence_support_ownership_20260902010001()
LANGUAGE plpgsql
AS $procedure$
DECLARE
    updated_rows INTEGER;
BEGIN
    LOOP
        PERFORM set_config('app.tx_mode', 'migration', true);
        PERFORM set_config('app.current_team_id', '', true);
        PERFORM set_config('app.current_profile_id', '', true);
        PERFORM set_config('app.allowed_space_ids', '', true);
        PERFORM set_config('app.known_evidence_support_ownership_backfill', 'true', true);

        WITH batch AS MATERIALIZED (
            SELECT support.ctid
            FROM relationship_evidence_supports AS support
            WHERE support.evidence_owner_profile_id IS NULL
            ORDER BY support.team_id, support.support_id
            LIMIT 500
            FOR UPDATE SKIP LOCKED
        )
        UPDATE relationship_evidence_supports AS support
        SET evidence_owner_profile_id = support.owner_profile_id
        FROM batch
        WHERE support.ctid = batch.ctid
          AND support.evidence_owner_profile_id IS NULL;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;

        COMMIT;
        EXIT WHEN updated_rows = 0;
    END LOOP;
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_known_evidence_support_ownership_20260902010001();
DROP POLICY relationship_supports_evidence_owner_backfill_update
    ON relationship_evidence_supports;
DROP PROCEDURE dense_mem_backfill_known_evidence_support_ownership_20260902010001();

DROP INDEX CONCURRENTLY IF EXISTS relationship_supports_evidence_owner_backfill_null_idx;

-- A validated check lets PostgreSQL prove the invariant without holding an
-- ACCESS EXCLUSIVE lock for a second full-table verification scan.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'relationship_evidence_supports'::regclass
          AND conname = 'relationship_supports_evidence_owner_not_null_check'
    ) THEN
        ALTER TABLE relationship_evidence_supports
            ADD CONSTRAINT relationship_supports_evidence_owner_not_null_check
            CHECK (evidence_owner_profile_id IS NOT NULL) NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_supports_evidence_owner_not_null_check;

ALTER TABLE relationship_evidence_supports
    ALTER COLUMN evidence_owner_profile_id SET NOT NULL;

ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT relationship_supports_evidence_owner_not_null_check;

ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_fragment_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_source_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_source_revision_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_source_id_source_revision_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_fragment_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_revision_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_revision_evidence_owner_fkey,
    ADD CONSTRAINT relationship_supports_fragment_evidence_owner_fkey
        FOREIGN KEY (team_id, fragment_id, evidence_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT relationship_supports_source_evidence_owner_fkey
        FOREIGN KEY (team_id, source_id, evidence_owner_profile_id)
        REFERENCES evidence_sources(team_id, source_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT relationship_supports_revision_evidence_owner_fkey
        FOREIGN KEY (team_id, source_revision_id, evidence_owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT relationship_supports_source_revision_evidence_owner_fkey
        FOREIGN KEY (team_id, source_id, source_revision_id, evidence_owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID;

ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_supports_fragment_evidence_owner_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_supports_source_evidence_owner_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_supports_revision_evidence_owner_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_supports_source_revision_evidence_owner_fkey;

DROP INDEX CONCURRENTLY IF EXISTS relationship_supports_evidence_owner_fragment_idx_invalid;

-- An interrupted concurrent build leaves an invalid catalog entry. Rename it
-- before retrying so the canonical name can be rebuilt safely.
-- +goose StatementBegin
DO $dense_mem_known_evidence_support_owner_index_recovery$
DECLARE
    candidate RECORD;
BEGIN
    FOR candidate IN
        SELECT index_class.relname
        FROM pg_index AS state
        JOIN pg_class AS index_class ON index_class.oid = state.indexrelid
        JOIN pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
        WHERE namespace.nspname = 'public'
          AND index_class.relname = 'relationship_supports_evidence_owner_fragment_idx'
          AND state.indisvalid IS FALSE
    LOOP
        EXECUTE format('ALTER INDEX public.%I RENAME TO %I', candidate.relname, candidate.relname || '_invalid');
    END LOOP;
END
$dense_mem_known_evidence_support_owner_index_recovery$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS relationship_supports_evidence_owner_fragment_idx
    ON relationship_evidence_supports(team_id, evidence_owner_profile_id, fragment_id);
DROP INDEX CONCURRENTLY IF EXISTS relationship_supports_evidence_owner_fragment_idx_invalid;

-- app_config is protected by a system-only RLS policy. Keep this setting and
-- update in one statement so NO TRANSACTION migrations do not lose the mode.
-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'system', true);
UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';
-- +goose StatementEnd

RESET lock_timeout;

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);
SET lock_timeout = '30s';

-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    cross_owner_count BIGINT;
BEGIN
    PERFORM set_config('app.tx_mode', 'migration', true);
    PERFORM set_config('app.current_team_id', '', true);
    PERFORM set_config('app.current_profile_id', '', true);
    PERFORM set_config('app.allowed_space_ids', '', true);

    SELECT count(*)
    INTO cross_owner_count
    FROM relationship_evidence_supports
    WHERE evidence_owner_profile_id IS DISTINCT FROM owner_profile_id;
    IF cross_owner_count > 0 THEN
        RAISE EXCEPTION
            'cannot roll back known-evidence support ownership: % cross-owner support rows would lose provenance',
            cross_owner_count;
    END IF;
END $$;

-- +goose StatementEnd

ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_supports_fragment_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_revision_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_revision_evidence_owner_fkey;

ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_supports_evidence_owner_not_null_check;

DROP TRIGGER IF EXISTS relationship_supports_evidence_owner_defaults ON relationship_evidence_supports;
DROP FUNCTION IF EXISTS dense_mem_relationship_support_evidence_owner_defaults();

-- Restore the strict append-only guard before removing the migration-only
-- column it references.
-- +goose StatementBegin
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
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS relationship_supports_evidence_owner_fragment_idx;

ALTER TABLE relationship_evidence_supports
    DROP COLUMN IF EXISTS evidence_owner_profile_id;

ALTER TABLE relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_fragment_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, fragment_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, source_id, owner_profile_id)
        REFERENCES evidence_sources(team_id, source_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_revision_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, source_revision_id, owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID,
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_id_source_revision_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT NOT VALID;

ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_evidence_supports_team_id_fragment_id_owner_profile_id_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_evidence_supports_team_id_source_id_owner_profile_id_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_evidence_supports_team_id_source_revision_id_owner_profile_id_fkey;
ALTER TABLE relationship_evidence_supports
    VALIDATE CONSTRAINT relationship_evidence_supports_team_id_source_id_source_revision_id_owner_profile_id_fkey;

-- +goose StatementBegin
SELECT set_config('app.tx_mode', 'system', true);
UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';
-- +goose StatementEnd

RESET lock_timeout;
