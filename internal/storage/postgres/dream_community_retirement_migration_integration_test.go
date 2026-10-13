//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const dreamCommunityRetirementBase = int64(20261011220000)
const dreamCommunityRetirementVersion = int64(20261013010000)

func TestDreamCommunityRetirementMigrationPreservesSettingsAndHistory(t *testing.T) {
	for _, configured := range []string{"true", "false", "absent"} {
		t.Run(configured, func(t *testing.T) {
			ctx := context.Background()
			db, cleanup := openMigrationSQLDB(t, ctx)
			defer cleanup()
			runGooseUpTo(t, ctx, db, migrationControlRetirementBaseVersion)
			seedMigrationControlRetirementFixture(t, ctx, db)
			runGooseUpTo(t, ctx, db, dreamCommunityRetirementBase)
			for range 2 {
				team, owner := insertMigrationTeamProfile(t, ctx, db)
				evidenceID := seedRetirementKnowledge(t, ctx, db, team, owner)
				require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
					for _, state := range []string{"queued", "running", "completed"} {
						_, err := tx.ExecContext(ctx, `INSERT INTO dream_cycle_runs
							(team_id,space_id,space_generation,initiated_by_profile_id,run_date,window_key,lane,status,
							created_hypotheses,outcome_summary,lease_token,lease_until)
							VALUES($1,dense_mem_team_shared_space($1),dense_mem_team_shared_generation($1),$2,
							'2026-10-12',$3,'graph',$3,2,'{"retained":2}',$4,now()+interval '1 hour')`, team, owner, state, uuid.NewString())
						if err != nil {
							return err
						}
					}
					for _, run := range []struct{ algorithm, status string }{{"louvain", "running"}, {"louvain", "completed"}, {"ontology_topic", "running"}} {
						_, err := tx.ExecContext(ctx, `INSERT INTO community_snapshot_runs
							(team_id,space_id,space_generation,window_key,algorithm_kind,status,node_count,lease_until)
							VALUES($1,dense_mem_team_shared_space($1),dense_mem_team_shared_generation($1),$2,$3,$4,7,now()+interval '1 hour')`,
							team, run.algorithm+":"+run.status, run.algorithm, run.status)
						if err != nil {
							return err
						}
					}
					var hypothesisID string
					if err := tx.QueryRowContext(ctx, `INSERT INTO hypotheses
						(team_id,space_id,space_generation,created_by_profile_id,status,statement,content_hash)
						VALUES($1,dense_mem_team_shared_space($1),dense_mem_team_shared_generation($1),$2,
						'proposed','Historical hypothesis remains inspectable.',$3) RETURNING hypothesis_id::text`, team, owner, "sha256:"+team).Scan(&hypothesisID); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO hypothesis_evidence_derivation_sources
						(team_id,hypothesis_id,space_id,space_generation,evidence_id,fragment_id,source_group_key,span_start,span_end,quote,authority)
						VALUES($1,$2,dense_mem_team_shared_space($1),dense_mem_team_shared_generation($1),$3,$3,'retained',0,5,'Atlas','primary')`, team, hypothesisID, evidenceID); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx, `INSERT INTO dream_evidence_target_attempts
						(team_id,target_evidence_id,target_content_hash,space_id,space_generation,pass_number,reservation_token,reservation_expires_at)
						VALUES($1,$2,'retained',dense_mem_team_shared_space($1),dense_mem_team_shared_generation($1),1,$3,now()+interval '1 hour')`, team, evidenceID, uuid.NewString())
					return err
				}))
			}
			require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `UPDATE app_config SET value='true' WHERE key IN ('DREAMING_ENABLED','DREAMING_FORCE_ENABLED','COMMUNITY_DETECTION_ENABLED')`); err != nil {
					return err
				}
				if configured == "absent" {
					_, err := tx.ExecContext(ctx, `DELETE FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`)
					return err
				}
				_, err := tx.ExecContext(ctx, `UPDATE app_config SET value=$1 WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`, configured)
				return err
			}))
			before := retirementHistorySnapshot(t, ctx, db)
			if configured == "true" {
				_, err := db.ExecContext(ctx, `CREATE FUNCTION reject_dream_retirement_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'retirement audit unavailable'; END; $$`)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `CREATE TRIGGER reject_dream_retirement_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_dream_retirement_audit()`)
				require.NoError(t, err)
				require.ErrorContains(t, migrationUpTo(ctx, db, dreamCommunityRetirementVersion), "retirement audit unavailable")
				assertRetirementCount(t, ctx, db, `SELECT count(*) FROM dream_cycle_runs WHERE status IN ('queued','running')`, 4)
				assertRetirementCount(t, ctx, db, `SELECT count(*) FROM app_config WHERE key='DREAMING_ENABLED' AND value='true'`, 1)
				_, err = db.ExecContext(ctx, `DROP TRIGGER reject_dream_retirement_audit ON audit_log; DROP FUNCTION reject_dream_retirement_audit()`)
				require.NoError(t, err)
			}
			runGooseUpTo(t, ctx, db, dreamCommunityRetirementVersion)
			require.Equal(t, before, retirementHistorySnapshot(t, ctx, db))
			expected := configured
			if expected == "absent" {
				expected = "false"
			}
			require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
				var enabled string
				if err := tx.QueryRowContext(ctx, `SELECT value FROM app_config WHERE key='ONTOLOGY_MAINTENANCE_ENABLED'`).Scan(&enabled); err != nil {
					return err
				}
				require.Equal(t, expected, enabled)
				return nil
			}))
			for _, check := range []struct {
				query string
				count int
			}{
				{`SELECT count(*) FROM dream_cycle_runs WHERE status='cancelled' AND lease_until IS NULL AND completed_at IS NOT NULL AND outcome_summary->>'retained'='2'`, 4},
				{`SELECT count(*) FROM community_snapshot_runs WHERE algorithm_kind='louvain' AND status='cancelled' AND lease_until IS NULL AND node_count=7`, 2},
				{`SELECT count(*) FROM community_snapshot_runs WHERE algorithm_kind='ontology_topic' AND status='running' AND lease_until>now()`, 2},
				{`SELECT count(*) FROM app_config WHERE key IN ('DREAMING_ENABLED','DREAMING_FORCE_ENABLED','COMMUNITY_DETECTION_ENABLED') AND value<>'false'`, 0},
				{`SELECT count(*) FROM dream_evidence_target_attempts WHERE status='abandoned' AND abandoned_at IS NOT NULL AND reservation_expires_at<=now()`, 2},
				{`SELECT count(*) FROM audit_log WHERE metadata->>'migration_version'='20261013010000' AND entity_type IN ('dream_cycle_run','community_run')`, 6},
			} {
				assertRetirementCount(t, ctx, db, check.query, check.count)
			}
			require.NoError(t, runtimeMigrationDownTo(ctx, db, dreamCommunityRetirementBase))
			runGooseUpTo(t, ctx, db, dreamCommunityRetirementVersion)
			assertRetirementCount(t, ctx, db, `SELECT count(*) FROM dream_cycle_runs WHERE status IN ('queued','running')`, 0)
			assertRetirementCount(t, ctx, db, `SELECT count(*) FROM audit_log WHERE metadata->>'migration_version'='20261013010000' AND entity_type IN ('dream_cycle_run','community_run')`, 6)
			require.Equal(t, before, retirementHistorySnapshot(t, ctx, db))
		})
	}
}

func retirementHistorySnapshot(t *testing.T, ctx context.Context, db *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, item := range []struct{ name, query string }{
			{"hypotheses", `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY hypothesis_id),'[]'::jsonb)::text FROM hypotheses row`},
			{"completed_dreams", `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY run_id),'[]'::jsonb)::text FROM dream_cycle_runs row WHERE status='completed'`},
			{"completed_communities", `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY run_id),'[]'::jsonb)::text FROM community_snapshot_runs row WHERE status='completed'`},
			{"evidence", `SELECT jsonb_agg(to_jsonb(row) ORDER BY fragment_id)::text FROM evidence_fragments row`},
			{"entities", `SELECT jsonb_agg(to_jsonb(row) ORDER BY entity_id)::text FROM entity_records row`},
			{"relationships", `SELECT jsonb_agg(to_jsonb(row) ORDER BY relationship_id)::text FROM relationship_records row`},
			{"supports", `SELECT jsonb_agg(to_jsonb(row) ORDER BY support_id)::text FROM relationship_evidence_supports row`},
			{"provenance", `SELECT jsonb_agg(to_jsonb(row) ORDER BY derivation_source_id)::text FROM hypothesis_evidence_derivation_sources row`},
		} {
			var snapshot string
			if err := tx.QueryRowContext(ctx, item.query).Scan(&snapshot); err != nil {
				return err
			}
			result[item.name] = snapshot
		}
		return nil
	}))
	return result
}

func seedRetirementKnowledge(t *testing.T, ctx context.Context, db *sql.DB, team, owner string) string {
	t.Helper()
	subject, object, ingest, evidence, relationship, observation, verification, support := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO team_predicate_definitions(team_id,predicate_key,version,aliases,allowed_subject_kinds,allowed_object_kinds,relationship_kind,current_cardinality,lifecycle_state,origin,metadata)
    SELECT $1,predicate_key,version,aliases,allowed_subject_kinds,allowed_object_kinds,relationship_kind,current_cardinality,lifecycle_state,'built_in',metadata FROM predicate_definitions WHERE predicate_key='uses' AND version=1 ON CONFLICT DO NOTHING`, []any{team}},
			{`INSERT INTO entity_records(team_id,entity_id,entity_kind) VALUES($1,$2,'project'),($1,$3,'product')`, []any{team, subject, object}},
			{`INSERT INTO knowledge_ingests(team_id,ingest_id,owner_profile_id,idempotency_key,request_hash,status) VALUES($1,$2,$3,'retained','retained','completed')`, []any{team, ingest, owner}},
			{`INSERT INTO evidence_fragments(team_id,fragment_id,ingest_id,owner_profile_id,evidence_index,content,content_hash) VALUES($1,$2,$3,$4,0,'Atlas uses PostgreSQL.','retained')`, []any{team, evidence, ingest, owner}},
			{`INSERT INTO relationship_records(team_id,relationship_id,owner_profile_id,semantic_group_key,subject_entity_id,predicate_key,predicate_version,object_entity_id,relationship_kind,current_cardinality,status,support_count,source_group_count)
    VALUES($1,$2,$3,'retained',$4,'uses',1,$5,'state','many','active',1,1)`, []any{team, relationship, owner, subject, object}},
			{`INSERT INTO relationship_observations(team_id,observation_id,relationship_id,ingest_id,owner_profile_id,subject_ref,original_predicate,object_ref)
    VALUES($1,$2,$3,$4,$5,'Atlas','uses','PostgreSQL')`, []any{team, observation, relationship, ingest, owner}},
			{`INSERT INTO verification_events(team_id,verification_event_id,observation_id,owner_profile_id,evidence_verdict) VALUES($1,$2,$3,$4,'entailed')`, []any{team, verification, observation, owner}},
			{`INSERT INTO relationship_evidence_supports(team_id,support_id,relationship_id,observation_id,verification_event_id,fragment_id,owner_profile_id,source_group_key,span_start,span_end,quote,authority)
    VALUES($1,$2,$3,$4,$5,$6,$7,'retained',0,22,'Atlas uses PostgreSQL.','primary')`, []any{team, support, relationship, observation, verification, evidence, owner}},
			{`INSERT INTO relationship_support_decision_events(team_id,support_id,relationship_id,owner_profile_id,actor_profile_id,decision,reason) VALUES($1,$2,$3,$4,$4,'grant','fixture')`, []any{team, support, relationship, owner}},
		}
		for _, item := range statements {
			if _, err := tx.ExecContext(ctx, item.query, item.args...); err != nil {
				return err
			}
		}
		return nil
	}))
	return evidence
}

func assertRetirementCount(t *testing.T, ctx context.Context, db *sql.DB, query string, expected int) {
	t.Helper()
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return fmt.Errorf("retirement assertion: %w", err)
		}
		require.Equal(t, expected, count, query)
		return nil
	}))
}
