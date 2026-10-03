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

func TestRetireEvidenceDiscoveryMigrationPreservesHistoryAndGraph(t *testing.T) {
	ctx := context.Background()
	db, cleanup := openMigrationSQLDB(t, ctx)
	defer cleanup()
	runGooseUpTo(t, ctx, db, migrationControlRetirementBaseVersion)
	seedMigrationControlRetirementFixture(t, ctx, db)
	runGooseUpTo(t, ctx, db, 20260927010002)
	teamID, ownerID := insertMigrationTeamProfile(t, ctx, db)
	otherTeamID, otherOwnerID := insertMigrationTeamProfile(t, ctx, db)
	var completedBefore, evaluationsBefore string
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		for _, team := range []struct{ team, owner string }{{teamID, ownerID}, {otherTeamID, otherOwnerID}} {
			for _, run := range []struct{ lane, status string }{{"graph", "running"}, {"evidence_discovery", "queued"}, {"evidence_discovery", "running"}, {"evidence_discovery", "completed"}} {
				_, err := tx.ExecContext(ctx, `
					INSERT INTO dream_cycle_runs (
						team_id, space_id, space_generation, initiated_by_profile_id, run_date,
						window_key, lane, status, created_hypotheses, outcome_summary, lease_token, lease_until
					) VALUES ($1, dense_mem_team_shared_space($1), dense_mem_team_shared_generation($1), $2,
						'2026-09-30', $3, $4, $5, 3, '{"retained":3}', $6, now() + interval '1 hour')
				`, team.team, team.owner, run.lane+":"+run.status, run.lane, run.status, uuid.NewString())
				if err != nil {
					return err
				}
			}
			ingest, fragment := uuid.NewString(), uuid.NewString()
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO knowledge_ingests(team_id, ingest_id, owner_profile_id, idempotency_key, request_hash, status)
				VALUES ($1, $2, $3, 'retirement-fixture', 'sha256:retirement-fixture', 'completed')
			`, team.team, ingest, team.owner); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_fragments(team_id, fragment_id, ingest_id, owner_profile_id, evidence_index, content, content_hash, source_type, authority)
				VALUES ($1, $2, $3, $4, 0, 'Retained evidence.', 'sha256:retained', 'manual', 'primary')
			`, team.team, fragment, ingest, team.owner); err != nil {
				return err
			}
			for pass, status := range []string{"reserved", "validated"} {
				_, err := tx.ExecContext(ctx, `
					INSERT INTO dream_evidence_target_attempts (
						team_id, target_evidence_id, target_content_hash, space_id, space_generation,
						pass_number, reservation_token, status, reservation_expires_at, validated_at
					) VALUES ($1, $2, 'sha256:retained', dense_mem_team_shared_space($1), dense_mem_team_shared_generation($1),
						$3, $4, $5, now() + interval '1 hour', CASE WHEN $5 = 'validated' THEN now() END)
				`, team.team, fragment, pass+1, uuid.NewString(), status)
				if err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO dream_evidence_target_evaluations (
					team_id, run_id, space_id, space_generation, target_evidence_id,
					target_content_hash, pass_number, provider_model, provider_turns,
					provider_proposals, accepted_proposals, created_hypotheses
				) SELECT $1, run_id, space_id, space_generation, $2, 'sha256:retained',
					2, 'legacy-model', 1, 3, 3, 3 FROM dream_cycle_runs
					WHERE team_id = $1 AND lane = 'evidence_discovery' AND status = 'running'
			`, team.team, fragment); err != nil {
				return err
			}

		}
		if err := tx.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(evaluation) ORDER BY evaluation_id)::text FROM dream_evidence_target_evaluations evaluation`).Scan(&evaluationsBefore); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(run) ORDER BY run_id)::text FROM dream_cycle_runs run WHERE status = 'completed'`).Scan(&completedBefore)
	}))
	runGooseUpTo(t, ctx, db, 20261003010001)
	require.NoError(t, execPostgresTxMode(ctx, db, "system", func(tx *sql.Tx) error {
		var completedAfter string
		if err := tx.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(run) ORDER BY run_id)::text FROM dream_cycle_runs run WHERE status = 'completed'`).Scan(&completedAfter); err != nil {
			return err
		}
		require.JSONEq(t, completedBefore, completedAfter)
		var evaluationsAfter string
		if err := tx.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(evaluation) ORDER BY evaluation_id)::text FROM dream_evidence_target_evaluations evaluation`).Scan(&evaluationsAfter); err != nil {
			return err
		}
		require.JSONEq(t, evaluationsBefore, evaluationsAfter, "append-only evaluations linked to retired runs must remain exact")

		for _, check := range []struct {
			query string
			count int
		}{
			{`SELECT count(*) FROM dream_cycle_runs WHERE lane = 'graph' AND status = 'running' AND lease_until > now()`, 2},
			{`SELECT count(*) FROM dream_cycle_runs WHERE lane = 'evidence_discovery' AND status = 'cancelled' AND completed_at IS NOT NULL AND lease_until IS NULL AND created_hypotheses = 3 AND outcome_summary->>'evidence_discovery_retired' = '1' AND outcome_summary->>'retained' = '3'`, 4},
			{`SELECT count(*) FROM dream_evidence_target_attempts WHERE status = 'abandoned' AND abandoned_at IS NOT NULL AND reservation_expires_at <= now()`, 2},
			{`SELECT count(*) FROM dream_evidence_target_attempts WHERE status = 'validated' AND validated_at IS NOT NULL AND reservation_expires_at > now()`, 2},
			{`SELECT count(*) FROM evidence_fragments`, 2},
		} {
			var count int
			if err := tx.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
				return fmt.Errorf("retirement assertion: %w", err)
			}
			require.Equal(t, check.count, count, check.query)
		}
		return nil
	}))
}
