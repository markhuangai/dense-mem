WITH scope AS (
    SELECT $1::uuid AS team_id,$2::uuid AS owner_id,$3::uuid AS space_id,$4::bigint AS generation,$5::uuid AS credential_id
), attempts AS (
    SELECT jsonb_build_object('team_id',b.team_id,'space_id',b.shared_space_id,'generation',b.space_generation,
        'window_id',a.window_id,'operation_key',COALESCE(eq.operation_key,'maintenance:'||b.batch_id::text),
        'assessment_id',a.assessment_id,'number',a.attempt,'admitted_at',a.admitted_at,
        'reconciled_at',a.reconciled_at,'terminal',a.reconciled_at IS NOT NULL AND (
            r.operation_key IS NOT NULL OR eq.operation_key IS NOT NULL OR (b.status IN ('completed','lost') AND b.failure_code<>'')
            OR EXISTS (SELECT 1 FROM topic_assignment_outcomes o WHERE o.team_id=b.team_id AND o.shared_space_id=b.shared_space_id AND o.space_generation=b.space_generation AND o.operation_id=b.batch_id)
            OR EXISTS (SELECT 1 FROM community_placement_outcomes o WHERE o.team_id=b.team_id AND o.shared_space_id=b.shared_space_id AND o.space_generation=b.space_generation AND o.operation_id=b.batch_id)),
        'usage_available',COALESCE(a.reported_input,0)>0 OR COALESCE(a.reported_output,0)>0 OR COALESCE(a.reported_total,0)>0,
        'input_tokens',COALESCE(a.reported_input,0),'output_tokens',COALESCE(a.reported_output,0),
        'total_tokens',COALESCE(a.reported_total,0),
        'outcome',COALESCE(NULLIF(r.body->'result'->>'failure_code',''),NULLIF(eq.body->'result'->>'failure_code',''),NULLIF(b.failure_code,''),b.status)) AS receipt
    FROM topic_maintenance_attempts a JOIN topic_maintenance_batches b ON b.batch_id=a.batch_id
    JOIN scope s ON s.team_id=b.team_id AND s.space_id=b.shared_space_id AND s.generation=b.space_generation
    LEFT JOIN historical_organization_assessments r ON r.team_id=b.team_id AND r.shared_space_id=b.shared_space_id
        AND r.space_generation=b.space_generation AND r.operation_key='maintenance:'||b.batch_id::text
        AND r.assessment_id=a.assessment_id
    LEFT JOIN LATERAL (
        SELECT e.operation_key,e.body FROM evidence_equivalence_outcomes e
        WHERE e.team_id=b.team_id AND e.shared_space_id=b.shared_space_id
          AND e.space_generation=b.space_generation AND e.assessment_id=a.assessment_id
          AND e.operation_key LIKE 'equivalence:'||b.batch_id::text||':%'
        ORDER BY e.operation_key LIMIT 1
    ) eq ON true
)
SELECT jsonb_build_object(
    'active_batches',(SELECT count(*) FROM topic_maintenance_batches b WHERE b.status='running'),
    'active_preparations',(SELECT count(*) FROM topic_maintenance_teams t WHERE t.lease_until>clock_timestamp()),
    'unreconciled_attempts',(SELECT count(*) FROM topic_maintenance_attempts a WHERE a.reconciled_at IS NULL),
    'budget_deferred',(SELECT count(*) FROM topic_maintenance_sources x WHERE x.eligible AND x.status='budget_deferred'),
    'foreign_work',(SELECT count(*) FROM topic_maintenance_batches b WHERE b.team_id<>s.team_id AND b.status='running'),
    'provider_attempts',COALESCE((SELECT jsonb_agg(receipt ORDER BY receipt->>'operation_key',receipt->>'assessment_id',receipt->>'number') FROM attempts),'[]'::jsonb)
)
FROM scope AS s
