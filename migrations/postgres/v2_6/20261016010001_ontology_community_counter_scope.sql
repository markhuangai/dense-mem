-- Lock/rewrite impact: policy metadata only; five-second DDL lock timeout and no table rewrite.
-- RLS impact: preserve system/migration access and team, shared-space, and current-generation bounds.
-- Backfill: none; retain existing counters.
-- Backward compatibility: equivalent visibility and mutation checks on the existing table.
-- Rollback: restore the previous policy without rewriting or deleting counters.
-- Resolve the shared-space frame once per statement, as ontology maintenance markers do.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER POLICY community_topic_scope ON community_topic_versions
USING (current_setting('app.tx_mode',true) IN ('system','migration') OR (
 current_setting('app.tx_mode',true) IN ('team','profile')
 AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
 AND (space_id,space_generation)=(SELECT space.id,space.generation FROM memory_spaces space
  WHERE space.team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid AND space.kind='team_shared')))
WITH CHECK (current_setting('app.tx_mode',true) IN ('system','migration') OR (
 current_setting('app.tx_mode',true) IN ('team','profile')
 AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
 AND (space_id,space_generation)=(SELECT space.id,space.generation FROM memory_spaces space
  WHERE space.team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid AND space.kind='team_shared')));

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER POLICY community_topic_scope ON community_topic_versions
USING (current_setting('app.tx_mode',true) IN ('system','migration') OR (
 current_setting('app.tx_mode',true) IN ('team','profile')
 AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
 AND space_id=dense_mem_team_shared_space(team_id)
 AND space_generation=dense_mem_team_shared_generation(team_id)))
WITH CHECK (current_setting('app.tx_mode',true) IN ('system','migration') OR (
 current_setting('app.tx_mode',true) IN ('team','profile')
 AND team_id=NULLIF(current_setting('app.current_team_id',true),'')::uuid
 AND space_id=dense_mem_team_shared_space(team_id)
 AND space_generation=dense_mem_team_shared_generation(team_id)));
