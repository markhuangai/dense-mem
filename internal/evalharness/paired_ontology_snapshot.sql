WITH scope AS (
    SELECT $1::uuid AS team_id,$2::uuid AS owner_id,$3::uuid AS space_id,$4::bigint AS generation,$5::uuid AS credential_id
), attempts AS (
    SELECT jsonb_build_object('team_id',b.team_id,'space_id',b.shared_space_id,'generation',b.space_generation,
        'window_id',a.window_id,'operation_key','maintenance:'||b.batch_id::text,
        'assessment_id',a.assessment_id,'number',a.attempt,'admitted_at',a.admitted_at,
        'reconciled_at',a.reconciled_at,'terminal',a.reconciled_at IS NOT NULL AND r.operation_key IS NOT NULL,
        'usage_available',COALESCE(a.reported_input,0)>0 OR COALESCE(a.reported_output,0)>0 OR COALESCE(a.reported_total,0)>0,
        'input_tokens',COALESCE(a.reported_input,0),'output_tokens',COALESCE(a.reported_output,0),
        'total_tokens',COALESCE(a.reported_total,0),
        'outcome',COALESCE(NULLIF(r.body->'result'->>'failure_code',''),NULLIF(b.failure_code,''),b.status)) AS receipt
    FROM ontology_maintenance_attempts a JOIN ontology_maintenance_batches b ON b.batch_id=a.batch_id
    JOIN scope s ON s.team_id=b.team_id AND s.space_id=b.shared_space_id AND s.generation=b.space_generation
    LEFT JOIN ontology_assessments r ON r.team_id=b.team_id AND r.shared_space_id=b.shared_space_id
        AND r.space_generation=b.space_generation AND r.operation_key='maintenance:'||b.batch_id::text
        AND r.assessment_id=a.assessment_id
)
SELECT jsonb_build_object(
    'active_batches',(SELECT count(*) FROM ontology_maintenance_batches b WHERE b.status='running'),
    'active_preparations',(SELECT count(*) FROM ontology_maintenance_teams t WHERE t.lease_until>clock_timestamp()),
    'active_projections',(SELECT count(*) FROM community_topic_work w WHERE w.status='building'),
    'unreconciled_attempts',(SELECT count(*) FROM ontology_maintenance_attempts a WHERE a.reconciled_at IS NULL),
    'budget_deferred',(SELECT count(*) FROM ontology_maintenance_sources x WHERE x.eligible AND x.status='budget_deferred'),
    'foreign_work',(SELECT count(*) FROM ontology_maintenance_batches b WHERE b.team_id<>s.team_id AND b.status='running'),
    'provider_attempts',COALESCE((SELECT jsonb_agg(receipt ORDER BY receipt->>'operation_key',receipt->>'assessment_id',receipt->>'number') FROM attempts),'[]'::jsonb),
    'legacy_window',(SELECT jsonb_build_object('id',w.window_id,'reserved_input',w.reserved_input,
        'reserved_output',w.reserved_output,'overrun',w.overrun) FROM ontology_maintenance_windows w
        WHERE w.starts_at<=clock_timestamp() AND w.ends_at>clock_timestamp() ORDER BY w.starts_at DESC LIMIT 1)
)
FROM scope AS s
