package postgres

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
       jsonb_build_object('content',content,'hash',content_hash,'authority',authority,
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
           'metadata',metadata::text,'supports',rows),
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
