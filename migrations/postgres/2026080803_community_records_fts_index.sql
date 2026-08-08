-- +goose NO TRANSACTION
-- +goose Up

-- The immutable helper keeps the expression index valid while allowing the
-- query and index expression to share one canonical text representation.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION community_record_search_vector(
    record_summary TEXT,
    record_top_entities TEXT[],
    record_top_predicates TEXT[]
)
RETURNS TSVECTOR
LANGUAGE SQL
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT to_tsvector(
        'simple'::regconfig,
        COALESCE(record_summary, '') || ' ' ||
        COALESCE(array_to_string(record_top_entities, ' '), '') || ' ' ||
        COALESCE(array_to_string(record_top_predicates, ' '), '')
    )
$$;
-- +goose StatementEnd

-- Lock/rewrite: concurrent index DDL avoids blocking normal community writes;
-- the index is derived from existing rows and does not rewrite the heap.
CREATE INDEX CONCURRENTLY IF NOT EXISTS community_records_current_fts_idx
    ON community_records USING GIN (
        community_record_search_vector(summary, top_entities, top_predicates)
    )
    WHERE status = 'current';

-- Lock/rewrite: adding NOT VALID records the invariant without scanning the
-- table; validation uses SHARE UPDATE EXCLUSIVE and permits normal reads and
-- writes. The validated check lets SET NOT NULL skip a second heap scan.
ALTER TABLE community_records
    DROP CONSTRAINT IF EXISTS community_records_logical_community_id_not_null;
ALTER TABLE community_records
    ADD CONSTRAINT community_records_logical_community_id_not_null
    CHECK (logical_community_id IS NOT NULL) NOT VALID;
ALTER TABLE community_records
    VALIDATE CONSTRAINT community_records_logical_community_id_not_null;
ALTER TABLE community_records
    ALTER COLUMN logical_community_id SET NOT NULL;
ALTER TABLE community_records
    DROP CONSTRAINT community_records_logical_community_id_not_null;

-- Recovery: a failed concurrent unique build leaves an invalid index behind;
-- this branch-only migration drops it before retrying the duplicate check.
DROP INDEX CONCURRENTLY IF EXISTS community_records_current_logical_unique;

-- +goose StatementBegin
DO $$
DECLARE
    duplicate_count BIGINT;
BEGIN
    SELECT count(*)
    INTO duplicate_count
    FROM (
        SELECT team_id, logical_community_id
        FROM community_records
        WHERE status = 'current'
        GROUP BY team_id, logical_community_id
        HAVING count(*) > 1
    ) AS duplicates;
    IF duplicate_count > 0 THEN
        RAISE EXCEPTION
            'cannot create community_records_current_logical_unique: % duplicate current team/logical community groups',
            duplicate_count;
    END IF;
END $$;
-- +goose StatementEnd

-- Lock/rewrite: concurrent index construction permits normal community reads
-- and writes while indexing the derived community tables.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS community_records_current_logical_unique
    ON community_records(team_id, logical_community_id)
    WHERE status = 'current';

CREATE INDEX CONCURRENTLY IF NOT EXISTS community_sources_group_idx
    ON community_sources(team_id, semantic_group_key, community_id);

-- This composite lookup supports the correlated community recall and
-- staleness probes without blocking normal community writes.
CREATE INDEX CONCURRENTLY IF NOT EXISTS community_sources_community_idx
    ON community_sources(team_id, community_id);

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS community_sources_community_idx;
DROP INDEX CONCURRENTLY IF EXISTS community_sources_group_idx;
DROP INDEX CONCURRENTLY IF EXISTS community_records_current_logical_unique;
DROP INDEX CONCURRENTLY IF EXISTS community_records_current_fts_idx;
DROP FUNCTION IF EXISTS community_record_search_vector(TEXT, TEXT[], TEXT[]);
ALTER TABLE community_records
    ALTER COLUMN logical_community_id DROP NOT NULL;
ALTER TABLE community_records
    DROP CONSTRAINT IF EXISTS community_records_logical_community_id_not_null;
