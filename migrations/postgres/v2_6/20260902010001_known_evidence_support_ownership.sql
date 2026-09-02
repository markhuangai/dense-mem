-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: this adds one nullable UUID, backfills it from the
-- existing Relationship owner, then makes it required. The backfill runs in
-- bounded, resumable batches so the append-only support table is not held in a
-- single transaction. The supporting index is built concurrently, and foreign
-- keys are installed NOT VALID before separate validation scans.
-- RLS impact: migration mode is the only mode allowed to rewrite the
-- append-only support table.
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

-- The support ledger is append-only at runtime. Each batch disables the
-- mutation guard only for its own update transaction, then commits before the
-- next batch is selected. A failed batch rolls back its trigger and row work;
-- rerunning the migration continues from the remaining NULL values.
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

        ALTER TABLE relationship_evidence_supports
            DISABLE TRIGGER relationship_supports_append_only;

        WITH batch AS MATERIALIZED (
            SELECT support.ctid
            FROM relationship_evidence_supports AS support
            WHERE support.evidence_owner_profile_id IS NULL
            ORDER BY support.ctid
            LIMIT 500
            FOR UPDATE SKIP LOCKED
        )
        UPDATE relationship_evidence_supports AS support
        SET evidence_owner_profile_id = support.owner_profile_id
        FROM batch
        WHERE support.ctid = batch.ctid
          AND support.evidence_owner_profile_id IS NULL;
        GET DIAGNOSTICS updated_rows = ROW_COUNT;

        ALTER TABLE relationship_evidence_supports
            ENABLE TRIGGER relationship_supports_append_only;

        COMMIT;
        EXIT WHEN updated_rows = 0;
    END LOOP;
END
$procedure$;
-- +goose StatementEnd

CALL dense_mem_backfill_known_evidence_support_ownership_20260902010001();
DROP PROCEDURE dense_mem_backfill_known_evidence_support_ownership_20260902010001();

ALTER TABLE relationship_evidence_supports
    ALTER COLUMN evidence_owner_profile_id SET NOT NULL;

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

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

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

DROP TRIGGER IF EXISTS relationship_supports_evidence_owner_defaults ON relationship_evidence_supports;
DROP FUNCTION IF EXISTS dense_mem_relationship_support_evidence_owner_defaults();

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

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

RESET lock_timeout;
