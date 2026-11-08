-- Lock/rewrite impact: teams policy DDL and eligible queue-row UPDATE locks; five-second lock-acquisition limit, no canonical rewrite.
-- RLS impact: temporary migration-only teams read policy; active current team-shared generations remain mandatory.
-- Backfill: one queue-only policy refresh, retaining assignments, receipts, fingerprints, and terminal failures/ambiguity.
-- Rollout: pause, drain, and stop old workers before applying; old workers must never consume the refreshed queue.
-- Recovery: forward only; restoring organized states could hide work completed or changed after this refresh.

-- +goose Up
SET LOCAL lock_timeout = '5s';
SELECT set_config('app.tx_mode','migration',true);
CREATE POLICY ontology_refinement_refresh_teams_read ON teams FOR SELECT
 USING (current_setting('app.tx_mode',true)='migration');
UPDATE ontology_maintenance_sources AS source
SET status='pending',reason='organization_policy_v2',revision=source.revision+1,
    pending_at=clock_timestamp(),updated_at=clock_timestamp()
FROM memory_spaces AS space,teams AS team
WHERE source.team_id=team.id AND team.status='active' AND team.deleted_at IS NULL
 AND space.team_id=source.team_id AND space.id=source.shared_space_id
 AND space.generation=source.space_generation AND space.kind='team_shared' AND space.lifecycle_state='active'
 AND source.eligible AND source.status='organized' AND source.source_kind IN ('entity','predicate');
DROP POLICY ontology_refinement_refresh_teams_read ON teams;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'ontology refinement refresh is forward-only; retain compatible workers and recover forward';
END $$;
-- +goose StatementEnd
