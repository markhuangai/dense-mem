-- Lock/rewrite impact: scoped marker-row locks with a five-second acquisition bound; no canonical table rewrite.
-- RLS impact: transaction-local migration authority preserves active team/shared-space/generation bounds.
-- Backfill: enqueue current active shared definitions for resumable refresh; no canonical or receipt backfill.
-- Backward compatibility: pause or disable, drain, and stop incompatible workers before either direction.
-- Rollback: Down enqueues the same refresh before restoring the prior matching binary; history and notifications remain intact.

-- +goose Up
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
SELECT dense_mem_enqueue_ontology_marker(head.team_id,head.shared_space_id,head.space_generation,
 'ontology_record_heads',head.record_id::text,'definition',head.record_id::text)
FROM ontology_record_heads AS head
JOIN memory_spaces AS space ON space.team_id=head.team_id AND space.id=head.shared_space_id
 AND space.generation=head.space_generation AND space.kind='team_shared' AND space.lifecycle_state='active'
JOIN teams AS team ON team.id=head.team_id AND team.status='active' AND team.deleted_at IS NULL
WHERE NOT head.retired AND head.kind IN ('entity_class','predicate_concept','topic')
ORDER BY head.team_id,head.shared_space_id,head.space_generation,head.record_id;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
SELECT dense_mem_enqueue_ontology_marker(head.team_id,head.shared_space_id,head.space_generation,
 'ontology_record_heads',head.record_id::text,'definition',head.record_id::text)
FROM ontology_record_heads AS head
JOIN memory_spaces AS space ON space.team_id=head.team_id AND space.id=head.shared_space_id
 AND space.generation=head.space_generation AND space.kind='team_shared' AND space.lifecycle_state='active'
JOIN teams AS team ON team.id=head.team_id AND team.status='active' AND team.deleted_at IS NULL
WHERE NOT head.retired AND head.kind IN ('entity_class','predicate_concept','topic')
ORDER BY head.team_id,head.shared_space_id,head.space_generation,head.record_id;

