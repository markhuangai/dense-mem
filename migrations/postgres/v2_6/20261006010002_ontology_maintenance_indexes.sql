-- +goose NO TRANSACTION

-- Lock/rewrite impact: concurrent index builds keep canonical writes available and do not rewrite rows.
-- RLS impact: scoped seek indexes leave transaction context and row visibility unchanged.
-- Backfill/recovery: PostgreSQL builds indexes over existing rows; interrupted builds are repaired before retry.
-- Backward compatibility: additive seek indexes preserve existing query results and require no application cutover.
-- Rollback: derived indexes can be dropped without modifying canonical or maintenance history.

-- +goose Up
SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS ontology_entity_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_evidence_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_relationship_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_predicate_relationship_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_source_evidence_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_ingest_evidence_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_support_relationship_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_maintenance_vocabulary_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_assessment_dependencies_idx_invalid;
-- +goose StatementBegin
DO $repair$
DECLARE index_name TEXT;
BEGIN
    FOREACH index_name IN ARRAY ARRAY['ontology_entity_seek_idx','ontology_evidence_seek_idx','ontology_relationship_seek_idx','ontology_predicate_relationship_seek_idx','ontology_source_evidence_seek_idx','ontology_ingest_evidence_seek_idx','ontology_support_relationship_seek_idx','ontology_maintenance_vocabulary_idx','ontology_assessment_dependencies_idx'] LOOP
        IF EXISTS(SELECT 1 FROM pg_index AS state JOIN pg_class AS name ON name.oid=state.indexrelid JOIN pg_namespace AS namespace ON namespace.oid=name.relnamespace
                  WHERE namespace.nspname='public' AND name.relname=index_name AND NOT state.indisvalid) THEN
            EXECUTE format('ALTER INDEX %I RENAME TO %I',index_name,index_name||'_invalid');
        END IF;
    END LOOP;
END $repair$;
-- +goose StatementEnd
DROP INDEX CONCURRENTLY IF EXISTS ontology_entity_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_evidence_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_relationship_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_predicate_relationship_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_source_evidence_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_ingest_evidence_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_support_relationship_seek_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_maintenance_vocabulary_idx_invalid;
DROP INDEX CONCURRENTLY IF EXISTS ontology_assessment_dependencies_idx_invalid;
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_entity_seek_idx ON entity_records(team_id,space_id,space_generation,entity_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_evidence_seek_idx ON evidence_fragments(team_id,space_id,space_generation,fragment_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_relationship_seek_idx ON relationship_records(team_id,space_id,space_generation,relationship_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_predicate_relationship_seek_idx ON relationship_records(team_id,space_id,space_generation,predicate_key,relationship_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_source_evidence_seek_idx ON evidence_fragments(team_id,space_id,space_generation,source_id,fragment_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_ingest_evidence_seek_idx ON evidence_fragments(team_id,space_id,space_generation,ingest_id,fragment_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_support_relationship_seek_idx ON relationship_evidence_supports(team_id,space_id,space_generation,relationship_id,fragment_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_maintenance_vocabulary_idx ON ontology_maintenance_sources USING GIN(vocabulary_words);
CREATE INDEX CONCURRENTLY IF NOT EXISTS ontology_assessment_dependencies_idx ON ontology_assessments USING GIN((body->'dependencies') jsonb_path_ops);

-- +goose Down
SET lock_timeout = '30s';
DROP INDEX CONCURRENTLY IF EXISTS ontology_assessment_dependencies_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_maintenance_vocabulary_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_support_relationship_seek_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_ingest_evidence_seek_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_source_evidence_seek_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_predicate_relationship_seek_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_relationship_seek_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_evidence_seek_idx;
DROP INDEX CONCURRENTLY IF EXISTS ontology_entity_seek_idx;
