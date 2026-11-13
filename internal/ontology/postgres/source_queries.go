package postgres

const vocabularyVectorSQL = `ts_delete(to_tsvector('simple',array_to_string(head.names,' ') || ' ' || COALESCE(revision.body->'definition'->>'description','')),?::text[])`

const maintenanceDefinitionPageSQL = `WITH RECURSIVE input AS (
 SELECT ?::uuid AS team_id,?::uuid AS shared_space_id,?::bigint AS space_generation,
 NULLIF(?,'')::uuid AS record_id,?::text AS source_kind,?::text AS source_id,
 ?::text AS after_kind,?::text AS after_id
` + maintenanceDefinitionSourcesSQL

const maintenanceDefinitionSourcesSQL = `), roots AS (
 SELECT head.record_id FROM ontology_record_heads AS head JOIN input AS scope
 USING(team_id,shared_space_id,space_generation) WHERE head.record_id=scope.record_id
 UNION
 SELECT dependency.record_id FROM ontology_source_dependencies AS dependency
 JOIN ontology_record_heads AS head ON head.team_id=dependency.team_id
 AND head.shared_space_id=dependency.shared_space_id AND head.space_generation=dependency.space_generation
 AND head.record_id=dependency.record_id AND head.version=dependency.record_version
 JOIN input AS scope ON scope.team_id=dependency.team_id AND scope.shared_space_id=dependency.shared_space_id
 AND scope.space_generation=dependency.space_generation
 WHERE dependency.source_kind=scope.source_kind AND dependency.source_id=scope.source_id AND NOT head.retired
), affected(record_id) AS (
 SELECT record_id FROM roots
 UNION
 SELECT dependency.record_id FROM ontology_revision_dependencies AS dependency
 JOIN affected ON affected.record_id=dependency.dependency_id
 JOIN ontology_record_heads AS head ON head.team_id=dependency.team_id
 AND head.shared_space_id=dependency.shared_space_id AND head.space_generation=dependency.space_generation
 AND head.record_id=dependency.record_id AND head.version=dependency.record_version
 JOIN input AS scope ON scope.team_id=dependency.team_id AND scope.shared_space_id=dependency.shared_space_id
 AND scope.space_generation=dependency.space_generation
), words AS (
 SELECT head.names || tsvector_to_array(` + vocabularyVectorSQL + `) AS terms
 FROM ontology_record_heads AS head JOIN ontology_revision_records AS revision
 USING(team_id,shared_space_id,space_generation,record_id,version)
 JOIN input AS scope USING(team_id,shared_space_id,space_generation)
 WHERE head.record_id=scope.record_id AND head.kind IN ('topic')
), keys AS (
 SELECT dependency.source_kind,dependency.source_id FROM affected
 JOIN ontology_source_dependencies AS dependency ON dependency.record_id=affected.record_id
 JOIN input AS scope USING(team_id,shared_space_id,space_generation)
 UNION
 SELECT source.source_kind,source.source_id FROM affected
 JOIN ontology_assessments AS assessment ON assessment.body->'dependencies' @>
 jsonb_build_array(jsonb_build_object('id',affected.record_id::text))
 JOIN input AS scope ON scope.team_id=assessment.team_id AND scope.shared_space_id=assessment.shared_space_id
 AND scope.space_generation=assessment.space_generation
 JOIN ontology_maintenance_sources AS source ON source.team_id=assessment.team_id
 AND source.shared_space_id=assessment.shared_space_id AND source.space_generation=assessment.space_generation
 AND source.assessment_id=assessment.assessment_id
 UNION
 SELECT source.source_kind,source.source_id FROM ontology_maintenance_sources AS source
 JOIN input AS scope USING(team_id,shared_space_id,space_generation)
 JOIN words ON source.vocabulary_words && words.terms
)
SELECT source.source_kind,source.source_id,source.source_version FROM keys
JOIN ontology_maintenance_sources AS source USING(source_kind,source_id)
JOIN input AS scope USING(team_id,shared_space_id,space_generation)
WHERE source.eligible AND (source.source_kind,source.source_id)>(scope.after_kind,scope.after_id)
ORDER BY source.source_kind,source.source_id LIMIT ?`

const maintenanceDefinitionGroupSQL = `WITH markers AS (
 SELECT * FROM jsonb_to_recordset(?::jsonb) AS marker(marker_index integer,record_id text,source_kind text,source_id text,after_kind text,after_id text)
), matches AS MATERIALIZED (
 SELECT marker.marker_index,matched.* FROM markers AS marker CROSS JOIN LATERAL (
 WITH RECURSIVE input AS (
 SELECT ?::uuid AS team_id,?::uuid AS shared_space_id,?::bigint AS space_generation,
 NULLIF(marker.record_id,'')::uuid AS record_id,marker.source_kind,marker.source_id,marker.after_kind,marker.after_id
` + maintenanceDefinitionSourcesSQL + `
 ) AS matched
), page AS (
 SELECT DISTINCT source_kind,source_id,source_version FROM matches ORDER BY source_kind,source_id LIMIT ?
)
SELECT matches.marker_index,matches.source_kind,matches.source_id,matches.source_version,page.source_id IS NOT NULL
FROM matches LEFT JOIN page USING(source_kind,source_id,source_version)
ORDER BY matches.source_kind,matches.source_id,matches.marker_index`

const evidenceSourceSQL = `
WITH candidate AS (
    SELECT fragment.*,source.current_revision_id
    FROM evidence_fragments AS fragment
    JOIN LATERAL (
        SELECT ingest.status,ingest.source_summary,ingest.metadata FROM knowledge_ingests AS ingest
        WHERE ingest.team_id=fragment.team_id AND ingest.ingest_id=fragment.ingest_id LIMIT 1
    ) AS ingest ON TRUE
    LEFT JOIN evidence_sources AS source ON source.team_id=fragment.team_id AND source.source_id=fragment.source_id
    WHERE fragment.team_id=?::uuid AND fragment.space_id=?::uuid AND fragment.space_generation=?
      AND fragment.fragment_id=ANY(?::uuid[]) AND ingest.status='completed'
      AND (fragment.source_id IS NULL OR source.current_revision_id=fragment.source_revision_id)
      AND NOT EXISTS (SELECT 1 FROM evidence_quarantines AS quarantine
           WHERE quarantine.team_id=fragment.team_id AND quarantine.fragment_id=fragment.fragment_id AND quarantine.status='active')
      AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events AS event
           WHERE event.team_id=fragment.team_id AND event.target_fragment_id=fragment.fragment_id)
      AND NOT (ingest.source_summary='overdue conflict deletion-only derivation'
           AND ingest.metadata->>'conflict_resolution_deletion_only'='true')
)
SELECT fragment_id::text,1::bigint,owner_profile_id::text,
       jsonb_build_object('content',content,'hash',content_hash,'authority',authority,'created_at',created_at::text,
           'source_id',COALESCE(source_id::text,''),'source_revision',COALESCE(source_revision_id::text,''),
           'current_revision',COALESCE(current_revision_id::text,''),'labels',labels::text,'metadata',metadata::text,
           'occurrences',COALESCE((SELECT jsonb_agg(jsonb_build_array(occurrence.occurrence_id,
               occurrence.owner_profile_id,occurrence.content_hash) ORDER BY occurrence.occurrence_id)::text
             FROM evidence_occurrences AS occurrence WHERE occurrence.team_id=candidate.team_id
               AND occurrence.space_id=candidate.space_id AND occurrence.space_generation=candidate.space_generation
               AND occurrence.canonical_fragment_id=candidate.fragment_id),'[]')),
       content_hash || ':' || owner_profile_id::text
FROM candidate`

const maintenanceEvidencePageSQL = `SELECT fragment_id::text AS id,1::bigint FROM evidence_fragments WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND fragment_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY fragment_id LIMIT ?`
