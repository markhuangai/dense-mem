package postgres

const maintenanceEntityPageSQL = `SELECT entity_id::text AS id,version FROM entity_records WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=? AND entity_id>COALESCE(NULLIF(?,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY entity_id LIMIT ?`

const maintenanceDefinitionPageSQL = `WITH RECURSIVE input AS (
 SELECT ?::uuid AS team_id,?::uuid AS shared_space_id,?::bigint AS space_generation,
 NULLIF(?,'')::uuid AS record_id,?::text AS source_kind,?::text AS source_id,
 ?::text AS after_kind,?::text AS after_id
), roots AS (
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
 SELECT head.names || tsvector_to_array(to_tsvector('simple',array_to_string(head.names,' ') || ' ' ||
 COALESCE(revision.body->'definition'->>'description',''))) AS terms
 FROM ontology_record_heads AS head JOIN ontology_record_revisions AS revision
 USING(team_id,shared_space_id,space_generation,record_id,version)
 JOIN input AS scope USING(team_id,shared_space_id,space_generation)
 WHERE head.record_id=scope.record_id AND head.kind IN ('entity_class','predicate_concept','topic')
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

const entitySourceSQL = `
WITH candidate AS (
    SELECT * FROM entity_records
    WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=?
      AND entity_id=?::uuid AND status='active'
)
SELECT candidate.version,''::text,candidate.entity_kind,
       jsonb_build_object('identity',candidate.identity_context::text,
           'names',COALESCE((SELECT jsonb_agg(jsonb_build_array(name.owner_profile_id,
                name.display_name,name.name_kind,name.locale) ORDER BY name.entity_name_id)::text
             FROM entity_names AS name WHERE name.team_id=candidate.team_id
               AND name.space_id=candidate.space_id AND name.space_generation=candidate.space_generation
               AND name.entity_id=candidate.entity_id AND name.valid_to IS NULL),'[]')),
       ''::text
FROM candidate`

const evidenceSourceSQL = `
WITH candidate AS (
    SELECT fragment.*,source.current_revision_id
    FROM evidence_fragments AS fragment
    JOIN knowledge_ingests AS ingest ON ingest.team_id=fragment.team_id AND ingest.ingest_id=fragment.ingest_id
    LEFT JOIN evidence_sources AS source ON source.team_id=fragment.team_id AND source.source_id=fragment.source_id
    WHERE fragment.team_id=?::uuid AND fragment.space_id=?::uuid AND fragment.space_generation=?
      AND fragment.fragment_id=?::uuid AND ingest.status='completed'
      AND (fragment.source_id IS NULL OR source.current_revision_id=fragment.source_revision_id)
      AND NOT EXISTS (SELECT 1 FROM evidence_quarantines AS quarantine
           WHERE quarantine.team_id=fragment.team_id AND quarantine.fragment_id=fragment.fragment_id AND quarantine.status='active')
      AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events AS event
           WHERE event.team_id=fragment.team_id AND event.target_fragment_id=fragment.fragment_id)
      AND NOT (ingest.source_summary='overdue conflict deletion-only derivation'
           AND ingest.metadata->>'conflict_resolution_deletion_only'='true')
)
SELECT 1::bigint,owner_profile_id::text,''::text,
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

const eligibleSupportsSQL = `
SELECT support.support_id,support.fragment_id,support.owner_profile_id,
       fragment.content_hash,decision.support_decision_id
FROM relationship_evidence_supports AS support
JOIN LATERAL (
    SELECT support_decision_id,decision FROM relationship_support_decision_events
    WHERE team_id=support.team_id AND support_id=support.support_id
    ORDER BY created_at DESC,support_decision_id DESC LIMIT 1
) AS decision ON decision.decision IN ('grant','reinstate')
JOIN evidence_fragments AS fragment ON fragment.team_id=support.team_id AND fragment.fragment_id=support.fragment_id
JOIN knowledge_ingests AS ingest ON ingest.team_id=fragment.team_id AND ingest.ingest_id=fragment.ingest_id
LEFT JOIN evidence_sources AS source ON source.team_id=support.team_id AND source.source_id=support.source_id
WHERE support.team_id=candidate.team_id AND support.space_id=candidate.space_id
  AND support.space_generation=candidate.space_generation AND support.relationship_id=candidate.relationship_id
  AND fragment.space_id=candidate.space_id AND fragment.space_generation=candidate.space_generation
  AND ingest.status='completed'
  AND (support.source_id IS NULL OR source.current_revision_id=support.source_revision_id)
  AND NOT EXISTS (SELECT 1 FROM evidence_quarantines AS quarantine
       WHERE quarantine.team_id=support.team_id AND quarantine.fragment_id=support.fragment_id AND quarantine.status='active')
  AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events AS event
       WHERE event.team_id=support.team_id AND event.target_fragment_id=support.fragment_id)`

// Equivalence also fences validity end and qualifications, which serve a different purpose from canonical recall group keys.
const relationshipSourceSQL = `
WITH candidate AS (
    SELECT * FROM relationship_records
    WHERE team_id=?::uuid AND space_id=?::uuid AND space_generation=?
      AND relationship_id=?::uuid AND status='active'
      AND support_count>0 AND identity_alias_of_relationship_id IS NULL
), supported AS (
    SELECT candidate.*,support.rows FROM candidate
    JOIN LATERAL (SELECT jsonb_agg(jsonb_build_array(support_id,fragment_id,owner_profile_id,
         content_hash,support_decision_id) ORDER BY support_id)::text AS rows
         FROM (` + eligibleSupportsSQL + `) AS eligible) AS support ON support.rows IS NOT NULL
)
SELECT version,owner_profile_id::text,''::text,
       jsonb_build_object('subject',subject_entity_id::text,'predicate',predicate_key,
           'predicate_version',predicate_version::text,'object_entity',COALESCE(object_entity_id::text,''),
           'object_value',COALESCE(object_value_id::text,''),'kind',relationship_kind,
           'cardinality',current_cardinality,'polarity',polarity,'scope',COALESCE(scope_key,''),
           'valid_from',COALESCE(extract(epoch FROM valid_from)::text,''),'valid_to',COALESCE(extract(epoch FROM valid_to)::text,''),
           'metadata',metadata::text,'supports',rows,
           'predicate_contract',COALESCE((SELECT jsonb_build_array(definition.allowed_subject_kinds,
               definition.allowed_object_kinds,definition.relationship_kind,definition.current_cardinality)::text
               FROM team_predicate_definitions AS definition WHERE definition.team_id=supported.team_id
                 AND definition.predicate_key=supported.predicate_key AND definition.version=supported.predicate_version),'')),
       jsonb_build_array(subject_entity_id,predicate_key,predicate_version,object_entity_id,
           object_value_id,relationship_kind,current_cardinality,polarity,scope_key,
           extract(epoch FROM valid_from),extract(epoch FROM valid_to),metadata)::text
FROM supported`

const predicateSourceSQL = `
WITH scope AS (SELECT ?::uuid AS team_id,?::uuid AS space_id,?::bigint AS generation),
candidate AS (SELECT definition.* FROM team_predicate_definitions AS definition,scope
    WHERE definition.team_id=scope.team_id AND definition.predicate_key=? AND definition.version=?
      AND definition.lifecycle_state='active'
      AND EXISTS (SELECT 1 FROM relationship_records AS candidate
          WHERE candidate.team_id=scope.team_id AND candidate.space_id=scope.space_id
            AND candidate.space_generation=scope.generation AND candidate.status='active'
            AND candidate.identity_alias_of_relationship_id IS NULL
            AND candidate.support_count>0 AND candidate.predicate_key=definition.predicate_key
            AND candidate.predicate_version=definition.version
            AND EXISTS (` + eligibleSupportsSQL + `)))
SELECT version,''::text,''::text,
       jsonb_build_object('aliases',aliases::text,'subject_kinds',allowed_subject_kinds::text,
          'object_kinds',allowed_object_kinds::text,'kind',relationship_kind,
          'cardinality',current_cardinality,'lifecycle',lifecycle_state,'metadata',metadata::text),
       ''::text FROM candidate`

const eligiblePredicateSelect = `SELECT DISTINCT ON (definition.predicate_key) definition.predicate_key AS id,definition.version
 FROM team_predicate_definitions AS definition JOIN relationship_records AS candidate
 ON candidate.team_id=definition.team_id AND candidate.predicate_key=definition.predicate_key AND candidate.predicate_version=definition.version
 WHERE candidate.team_id=?::uuid AND candidate.space_id=?::uuid AND candidate.space_generation=?
 AND candidate.status='active' AND candidate.support_count>0 AND candidate.identity_alias_of_relationship_id IS NULL
 AND definition.lifecycle_state='active' AND EXISTS (` + eligibleSupportsSQL + `)`
