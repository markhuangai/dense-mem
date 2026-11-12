//go:build integration

package postgres

import (
	"context"

	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceLargeBacklogAndFairTurns(t *testing.T) {
	started := time.Now()
	f := newOrganizationFixture(t)
	ctx := context.Background()
	template := f.organizationEvidence(t, 0, "Backlog template", nil)
	for first := 1; first <= 25001; first += 1000 {
		last := min(first+999, 25001)
		require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO evidence_fragments
 SELECT (jsonb_populate_record(NULL::evidence_fragments,to_jsonb(template)||jsonb_build_object(
 'fragment_id',gen_random_uuid(),'evidence_index',index,'content','Backlog evidence '||index,
 'content_hash','sha256:'||encode(sha256(convert_to('Backlog evidence '||index,'UTF8')),'hex'),'force_insert',true))).*
 FROM evidence_fragments AS template CROSS JOIN generate_series(?::int,?::int) AS index
 WHERE template.team_id=?::uuid AND template.fragment_id=?::uuid`, first, last, f.team, template.ID).Error
		}))
	}
	other := maintenanceOtherTeam(t, f)
	other.organizationEvidence(t, 0, "Small team's pending work.", nil)
	third := maintenanceOtherTeam(t, f)
	third.organizationEvidence(t, 0, "Third team's pending work.", nil)
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyStartTime: time.Now().UTC().Format("15:04")}, "control", "", "")
	require.NoError(t, err)
	window := maintenanceWindow(t, f, config)
	first, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Hour)
	require.NoError(t, err)
	require.NotNil(t, first)
	_, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.store.discoverMaintenanceError(ctx, *first, ontology.MaintenancePageSize))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *first))
	second, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Hour)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotEqual(t, first.TeamID, second.TeamID, "one team turn cannot consume another team's position")
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *second))
	var big *ontology.MaintenanceTurn
	for range 4 {
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Hour)
		require.NoError(t, err)
		require.NotNil(t, turn)
		if turn.TeamID == f.team {
			big = turn
			break
		}
		require.NoError(t, f.store.discoverMaintenanceError(ctx, *turn, ontology.MaintenancePageSize))
		require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	}
	require.NotNil(t, big)
	var discovered bool
	for range 800 {
		require.NoError(t, f.store.discoverMaintenanceError(ctx, *big, ontology.MaintenancePageSize))
		require.NoError(t, f.admin.Raw(`SELECT discovery_kind=1 FROM ontology_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, f.team, f.space, f.generation).Row().Scan(&discovered))
		if discovered {
			break
		}
	}
	require.True(t, discovered, "bounded backfill must discover the entire large corpus")
	status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, status.CoverageComplete)
	var evidenceCount int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND eligible AND source_kind='evidence'`, f.team).Row().Scan(&evidenceCount))
	require.Greater(t, evidenceCount, 25000)
	unrelated := testTopic("unrelated-large-marker")
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("sparse-large-marker", 0, ontology.Change{Record: unrelated}))
	require.NoError(t, err)
	require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
		handles, err := maintenanceMarkerSources(tx, fence, maintenanceMarker{AnchorKind: "ontology_record_heads", TargetKind: "definition", TargetID: unrelated.ID}, 100)
		if err != nil {
			return err
		}
		require.Empty(t, handles, "an unrelated definition must not visit the 25k-source backlog")
		return nil
	}))
	var cursor string
	require.NoError(t, f.admin.Raw(`SELECT fragment_id::text FROM evidence_fragments WHERE team_id=?::uuid ORDER BY fragment_id OFFSET 24900 LIMIT 1`, f.team).Row().Scan(&cursor))
	assertNativeMaintenanceSeek(t, f, cursor)
	claim, err := f.store.ClaimMaintenanceBatch(ctx, *big, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.LessOrEqual(t, len(claim.Sources), 20)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "window_closed", time.Now().UTC()))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *big))
	restarted := NewStore(f.app, f.rls)
	next, err := restarted.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotEqual(t, f.team, next.TeamID)
	require.NoError(t, restarted.ReleaseMaintenanceTurn(ctx, *next))
	service, calls := maintenanceServiceFixture(t, f, config, nil)
	for range 16 {
		_, err := service.RunTurn(ctx)
		require.NoError(t, err)
		var organized int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, other.team).Row().Scan(&organized))
		var thirdOrganized int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, third.team).Row().Scan(&thirdOrganized))
		if organized > 0 && thirdOrganized > 0 {
			break
		}
	}
	var organized, pending int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, other.team).Row().Scan(&organized))
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='pending'`, f.team).Row().Scan(&pending))
	require.Greater(t, organized, 0)
	require.Greater(t, pending, 20000)
	require.Greater(t, calls(), int32(0))
	var thirdOrganized int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, third.team).Row().Scan(&thirdOrganized))
	require.Greater(t, thirdOrganized, 0)
	t.Logf("A/B/C backlog progress: pending=%d small_teams_organized=%d elapsed=%s", pending, organized+thirdOrganized, time.Since(started))
}

func TestOntologyMaintenanceOversizedGroupClosure(t *testing.T) {
	f := newOrganizationFixture(t)
	var sources []ontology.SourceHandle
	var dependencies []ontology.SourceDependency
	for range 21 {
		handle := f.organizationEvidence(t, 0, "Complete group source.", nil)
		sources = append(sources, handle)
		dependencies = append(dependencies, f.source(t, handle))
	}
	group := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.GroupTogether, Members: sources}, Sources: dependencies}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("large-group", 0, ontology.Change{Record: group}))
	require.NoError(t, err)
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	for range 8 {
		require.NoError(t, f.store.discoverMaintenanceError(context.Background(), *turn, ontology.MaintenancePageSize))
	}
	claim, err := f.store.ClaimMaintenanceBatch(context.Background(), *turn, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Nil(t, claim)
	status, err := f.store.MaintenanceStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Ambiguous)
	require.False(t, status.CoverageComplete)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
}
