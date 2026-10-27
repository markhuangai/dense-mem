WITH scope AS (
    SELECT $1::uuid AS team_id, $2::uuid AS owner_id, $3::uuid AS space_id,
           $4::bigint AS generation, $5::uuid AS credential_id
), imports AS (
    SELECT jsonb_build_object(
        'scope', jsonb_build_object('team_id', a.team_id, 'owner_id', a.owner_profile_id,
            'space_id', a.space_id, 'generation', a.space_generation),
        'source_doc_id', substring(a.idempotency_key FROM 6), 'attempt_id', a.attempt_id,
        'request_hash', a.request_hash, 'outcome', a.outcome,
        'evidence_ids', COALESCE((SELECT jsonb_agg(e->>'evidence_id' ORDER BY e->>'evidence_id')
            FROM jsonb_array_elements(a.public_result->'evidence') e WHERE e->>'disposition'='stored'), '[]'::jsonb),
        'content_hashes', COALESCE((SELECT jsonb_agg(o.content_hash ORDER BY o.evidence_index)
            FROM jsonb_array_elements(a.public_result->'evidence') e JOIN evidence_occurrences o
              ON o.team_id=a.team_id AND o.ingest_id=a.attempt_id AND o.owner_profile_id=a.owner_profile_id
                AND o.canonical_fragment_id=NULLIF(e->>'evidence_id','')::uuid
                AND o.evidence_index=NULLIF(e->>'evidence_index','')::integer
            WHERE e->>'disposition'='stored'), '[]'::jsonb),
        'live', a.outcome='completed' AND EXISTS (
            SELECT 1 FROM jsonb_array_elements(a.public_result->'evidence') e
            WHERE e->>'disposition'='stored'
        ) AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements(a.public_result->'evidence') e
            LEFT JOIN evidence_fragments f ON f.team_id=a.team_id AND f.fragment_id=NULLIF(e->>'evidence_id','')::uuid
                AND f.space_id=a.space_id AND f.space_generation=a.space_generation
                AND f.space_generation=dense_mem_active_space_generation(f.team_id,f.space_id)
            LEFT JOIN evidence_occurrences o ON o.team_id=a.team_id AND o.ingest_id=a.attempt_id
                AND o.owner_profile_id=a.owner_profile_id AND o.canonical_fragment_id=f.fragment_id
                AND o.evidence_index=NULLIF(e->>'evidence_index','')::integer
                AND o.space_id=a.space_id AND o.space_generation=a.space_generation
            WHERE e->>'disposition'='stored' AND (f.fragment_id IS NULL OR o.occurrence_id IS NULL)
        )) AS receipt
    FROM remember_attempts a JOIN scope s ON s.team_id=a.team_id
    WHERE a.submission_kind='remember' AND a.idempotency_key LIKE 'eval:%'
), canonical AS (
    SELECT 'evidence_fragments' AS kind, to_jsonb(r)::text AS body FROM evidence_fragments r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'evidence_occurrences',to_jsonb(r)::text FROM evidence_occurrences r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'evidence_sources',to_jsonb(r)::text FROM evidence_sources r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'evidence_source_revisions',to_jsonb(r)::text FROM evidence_source_revisions r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'entity_records',to_jsonb(r)::text FROM entity_records r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'entity_names',to_jsonb(r)::text FROM entity_names r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'value_records',to_jsonb(r)::text FROM value_records r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'relationship_records',to_jsonb(r)::text FROM relationship_records r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'relationship_evidence_supports',to_jsonb(r)::text FROM relationship_evidence_supports r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'relationship_support_decision_events',to_jsonb(r)::text FROM relationship_support_decision_events r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'evidence_lifecycle_events',to_jsonb(r)::text FROM evidence_lifecycle_events r,scope s WHERE r.team_id=s.team_id
    UNION ALL SELECT 'evidence_quarantines',to_jsonb(r)::text FROM evidence_quarantines r,scope s WHERE r.team_id=s.team_id
)
SELECT jsonb_build_object(
    'observed_at',clock_timestamp(),
    'scope',jsonb_build_object('team_id',s.team_id,'owner_id',s.owner_id,'space_id',s.space_id,'generation',s.generation),
    'scope_verified',EXISTS(SELECT 1 FROM memory_spaces m JOIN teams t ON t.id=m.team_id
        WHERE m.team_id=s.team_id AND m.id=s.space_id AND m.generation=s.generation
          AND m.kind='team_shared' AND m.lifecycle_state='active' AND t.status='active' AND t.deleted_at IS NULL)
        AND EXISTS(SELECT 1 FROM ownership_aliases a WHERE a.team_id=s.team_id
          AND a.legacy_owner_id=s.owner_id AND a.credential_id=s.credential_id),
    'foreign_sources',(SELECT count(*) FROM evidence_fragments f WHERE f.team_id<>s.team_id),
    'evidence_count',(SELECT count(*) FROM evidence_fragments f WHERE f.team_id=s.team_id),
    'canonical_count',(SELECT count(*) FROM canonical),
    'unclaimed_evidence',(SELECT count(*) FROM evidence_fragments f WHERE f.team_id=s.team_id
        AND NOT EXISTS(SELECT 1 FROM remember_attempts a CROSS JOIN LATERAL jsonb_array_elements(a.public_result->'evidence') e
            WHERE a.team_id=f.team_id AND a.submission_kind='remember' AND a.idempotency_key LIKE 'eval:%'
              AND a.outcome='completed' AND e->>'evidence_id'=f.fragment_id::text)),
    'imports',COALESCE((SELECT jsonb_agg(receipt ORDER BY receipt->>'source_doc_id',receipt->>'attempt_id') FROM imports),'[]'::jsonb),
    'canonical_hash','sha256:'||encode(sha256(convert_to(COALESCE((SELECT jsonb_agg(jsonb_build_array(kind,body) ORDER BY kind,body)::text FROM canonical),'[]'),'UTF8')),'hex'),
    'source_hash','sha256:'||encode(sha256(convert_to(COALESCE((SELECT jsonb_agg(receipt ORDER BY receipt->>'source_doc_id',receipt->>'attempt_id')::text FROM imports),'[]'),'UTF8')),'hex')
)
FROM scope AS s
