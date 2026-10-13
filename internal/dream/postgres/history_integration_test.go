//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	dream "github.com/markhuangai/dense-mem/internal/dream"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRetiredDreamHistoryPreservesProvenanceIsolationAndRetention(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	team := createLedgerTeam(t, admin, rls, "historical-dream-a")
	owner := createLedgerProfile(t, admin, rls, team, "historical-owner-a")
	peer := createLedgerProfile(t, admin, rls, team, "historical-owner-b")
	foreign := createLedgerTeam(t, admin, rls, "historical-dream-c")
	run, hypothesis, evidence, ingest, capture := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		for _, item := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO knowledge_ingests(team_id,ingest_id,owner_profile_id,status) VALUES(?::uuid,?::uuid,?::uuid,'completed')`, []any{team, ingest, owner}},
			{`INSERT INTO evidence_fragments(team_id,fragment_id,ingest_id,owner_profile_id,evidence_index,content,content_hash) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,0,'Historical exact evidence.','retained')`, []any{team, evidence, ingest, owner}},
			{`INSERT INTO dream_cycle_runs(team_id,run_id,initiated_by_profile_id,run_date,window_key,status,lane) VALUES(?::uuid,?::uuid,?::uuid,'2026-10-12','historical','completed','evidence_discovery')`, []any{team, run, peer}},
			{`INSERT INTO hypotheses(team_id,hypothesis_id,created_by_profile_id,status,statement,content_hash,cycle_run_id,lane) VALUES(?::uuid,?::uuid,?::uuid,'proposed','Historical hypothesis.','retained',?::uuid,'evidence_discovery')`, []any{team, hypothesis, peer, run}},
			{`INSERT INTO hypothesis_evidence_derivation_sources(team_id,hypothesis_id,space_id,space_generation,evidence_id,fragment_id,source_group_key,span_start,span_end,quote,authority) VALUES(?::uuid,?::uuid,dense_mem_team_shared_space(?::uuid),dense_mem_team_shared_generation(?::uuid),?::uuid,?::uuid,'retained',0,25,'Historical exact evidence.','primary')`, []any{team, hypothesis, team, team, evidence, evidence}},
			{`INSERT INTO dream_diagnostic_captures(team_id,capture_id,run_id,hypothesis_id,phase,outcome,payload,details,capture_state,created_at,expires_at) VALUES(?::uuid,?::uuid,?::uuid,?::uuid,'provider','completed','{"response":"retained"}','{"historical":true}','captured',now()-interval '2 days',now()-interval '1 day')`, []any{team, capture, run, hypothesis}},
		} {
			if err := tx.Exec(item.query, item.args...).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	store := NewStore(app, rls)
	service := dream.NewControl(dream.ControlDependencies{Store: store})
	status, err := service.Status(ctx, team)
	require.NoError(t, err)
	require.True(t, status.Retired)
	require.Equal(t, 1, status.HistoricalProposedCount)
	item, err := service.Get(ctx, team, hypothesis)
	require.NoError(t, err)
	require.Equal(t, []string{evidence}, item.SourceEvidenceIDs)
	require.Len(t, item.EvidenceDerivations, 1)
	require.Equal(t, "Historical exact evidence.", item.EvidenceDerivations[0].Quote)
	_, err = service.Get(ctx, foreign, hypothesis)
	require.ErrorIs(t, err, dream.ErrDreamNotFound)
	page, err := store.ListDreamDiagnostics(ctx, dreamcontract.DreamDiagnosticListInput{TeamID: foreign, RunID: run})
	require.NoError(t, err)
	require.Empty(t, page.Items)
	diagnostic, err := store.GetDreamDiagnostic(ctx, team, run, capture)
	require.NoError(t, err)
	require.Empty(t, diagnostic.Payload)
	require.Equal(t, "expired", diagnostic.CaptureState)
	purged, err := store.PurgeExpiredDreamDiagnostics(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, purged)
	retained, err := service.Get(ctx, team, hypothesis)
	require.NoError(t, err)
	require.Equal(t, item, retained)
}
