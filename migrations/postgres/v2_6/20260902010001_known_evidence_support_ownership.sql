-- +goose Up
-- +goose StatementBegin

-- Lock/rewrite impact: this adds one nullable UUID, backfills it from the
-- existing Relationship owner, then makes it required. The supporting index is
-- additive. RLS impact: migration mode is already the only mode allowed to
-- rewrite the append-only support table.
-- Backward compatibility: existing support rows retain their current owner;
-- new known-evidence rows can point at a different evidence owner while the
-- Relationship and support-decision owner remain unchanged.
-- Rollback: the down migration refuses to discard cross-owner provenance.

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

ALTER TABLE relationship_evidence_supports
    ADD COLUMN IF NOT EXISTS evidence_owner_profile_id UUID NULL;

-- The support ledger is append-only at runtime. This migration-only backfill
-- preserves existing rows while the surrounding transaction keeps the guard
-- disabled and restores it before any application transaction can run.
ALTER TABLE relationship_evidence_supports
    DISABLE TRIGGER relationship_supports_append_only;

UPDATE relationship_evidence_supports AS support
SET evidence_owner_profile_id = support.owner_profile_id
WHERE support.evidence_owner_profile_id IS NULL;

ALTER TABLE relationship_evidence_supports
    ENABLE TRIGGER relationship_supports_append_only;

ALTER TABLE relationship_evidence_supports
    ALTER COLUMN evidence_owner_profile_id SET NOT NULL;

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

ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_fragment_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_source_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_source_revision_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_evidence_supports_team_id_source_id_source_revision_id_owner_profile_id_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_fragment_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_revision_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_revision_evidence_owner_fkey;

ALTER TABLE relationship_evidence_supports
    ADD CONSTRAINT relationship_supports_fragment_evidence_owner_fkey
        FOREIGN KEY (team_id, fragment_id, evidence_owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT relationship_supports_source_evidence_owner_fkey
        FOREIGN KEY (team_id, source_id, evidence_owner_profile_id)
        REFERENCES evidence_sources(team_id, source_id, owner_profile_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT relationship_supports_revision_evidence_owner_fkey
        FOREIGN KEY (team_id, source_revision_id, evidence_owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT relationship_supports_source_revision_evidence_owner_fkey
        FOREIGN KEY (team_id, source_id, source_revision_id, evidence_owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS relationship_supports_evidence_owner_fragment_idx
    ON relationship_evidence_supports(team_id, evidence_owner_profile_id, fragment_id);

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

SELECT set_config('app.tx_mode', 'migration', true);
SELECT set_config('app.current_team_id', '', true);
SELECT set_config('app.current_profile_id', '', true);
SELECT set_config('app.allowed_space_ids', '', true);

DO $$
DECLARE
    cross_owner_count BIGINT;
BEGIN
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

ALTER TABLE relationship_evidence_supports
    DROP CONSTRAINT IF EXISTS relationship_supports_fragment_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_revision_evidence_owner_fkey,
    DROP CONSTRAINT IF EXISTS relationship_supports_source_revision_evidence_owner_fkey;

DROP TRIGGER IF EXISTS relationship_supports_evidence_owner_defaults ON relationship_evidence_supports;
DROP FUNCTION IF EXISTS dense_mem_relationship_support_evidence_owner_defaults();

DROP INDEX IF EXISTS relationship_supports_evidence_owner_fragment_idx;

ALTER TABLE relationship_evidence_supports
    DROP COLUMN IF EXISTS evidence_owner_profile_id;

ALTER TABLE relationship_evidence_supports
    ADD CONSTRAINT relationship_evidence_supports_team_id_fragment_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, fragment_id, owner_profile_id)
        REFERENCES evidence_fragments(team_id, fragment_id, owner_profile_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, source_id, owner_profile_id)
        REFERENCES evidence_sources(team_id, source_id, owner_profile_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_revision_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, source_revision_id, owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT relationship_evidence_supports_team_id_source_id_source_revision_id_owner_profile_id_fkey
        FOREIGN KEY (team_id, source_id, source_revision_id, owner_profile_id)
        REFERENCES evidence_source_revisions(team_id, source_id, source_revision_id, owner_profile_id)
        ON DELETE RESTRICT;

UPDATE app_config
SET value = regexp_replace(
        to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        '\.?0+Z$',
        'Z'
    ),
    updated_at = clock_timestamp()
WHERE key = 'update_time';

-- +goose StatementEnd
