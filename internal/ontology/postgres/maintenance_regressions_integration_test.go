//go:build integration

package postgres

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceRunCompletionUsesActiveScope(t *testing.T) {
	for _, failedStatus := range []string{"failed", "ambiguous", "budget_deferred"} {
		t.Run(failedStatus, func(t *testing.T) {
			f := newOrganizationFixture(t)
			f.organizationEvidence(t, 0, "Historical generation evidence.", nil)
			config := maintenanceSettings(t, f)
			window := maintenanceWindow(t, f, config)
			ctx := context.Background()
			turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
			require.NoError(t, err)
			require.NotNil(t, turn)
			for range 6 {
				require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
			}
			require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
				if err := tx.Exec(`UPDATE ontology_maintenance_sources SET status=?,last_run_id=?::uuid WHERE team_id=?::uuid`, failedStatus, turn.RunID, f.team).Error; err != nil {
					return err
				}
				return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
			}))
			require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
			service, calls := maintenanceServiceFixture(t, f, config, nil)
			drainMaintenance(t, service)
			status, err := service.Status(ctx)
			require.NoError(t, err)
			require.True(t, status.CoverageComplete)
			require.Zero(t, status.Counts.Eligible)
			require.Zero(t, calls())
			var runStatus string
			require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_runs WHERE run_id=?::uuid`, turn.RunID).Row().Scan(&runStatus))
			require.Equal(t, "completed", runStatus)
			_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "retry", OperationKey: "historical-retry", RetryRunID: turn.RunID}, time.Now().UTC())
			require.ErrorIs(t, err, ontology.ErrInvalid)
		})
	}
}

func TestOntologyMaintenanceDefinitionMarkersTargetReceiptsAndVocabulary(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	support := f.organizationEvidence(t, 0, "Evidence supporting the taxonomy.", nil)
	dependent := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
	unrelated := f.organizationEvidence(t, 0, "Vega writes Swift.", nil)
	root := testTopic("systems")
	root.Sources = []ontology.SourceDependency{f.source(t, support)}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("marker-root", 0, ontology.Change{Record: root}))
	require.NoError(t, err)
	child := testTopic("postgresql")
	child.Definition.ParentID = root.ID
	child.Dependencies = []ontology.RevisionRef{{ID: root.ID, Version: 1}}
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("marker-child", 1, ontology.Change{Record: child}))
	require.NoError(t, err)
	config := maintenanceSettings(t, f)
	service, _ := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, response *assessment.Response) { response.Items = nil })
	for range 30 {
		progress, _ := service.RunTurn(ctx)
		if !progress {
			break
		}
	}
	status, err := service.Status(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, status.Counts.Failed)
	selectSources := func(marker maintenanceMarker) []ontology.SourceHandle {
		t.Helper()
		var handles []ontology.SourceHandle
		require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
			var err error
			handles, err = maintenanceMarkerSources(tx, fence, marker, ontology.MaintenancePageSize)
			return err
		}))
		return handles
	}
	handles := selectSources(maintenanceMarker{AnchorKind: "source_dependency", AnchorID: ontology.SourceKey(support), TargetKind: "definition", TargetID: support.ID})
	require.ElementsMatch(t, []ontology.SourceHandle{support, dependent}, handles, "a parent support change must reach failed receipts without scanning unrelated sources")
	page, err := f.store.ListRecords(ctx, f.team, "", "", 1)
	require.NoError(t, err)
	topic := testTopic("swift")
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("new-vocabulary", page.Revision, ontology.Change{Record: topic}))
	require.NoError(t, err)
	handles = selectSources(maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: topic.ID, TargetKind: "definition", TargetID: topic.ID})
	require.Equal(t, []ontology.SourceHandle{unrelated}, handles, "new vocabulary must reach a failed source with no previous dependency on the new record")
}

func TestOntologyMaintenanceTransitiveInvalidation(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Evidence supporting database vocabulary.", nil)
	b := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
	topic := testTopic("postgresql")
	topic.Sources = []ontology.SourceDependency{f.source(t, a)}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("supported-topic", 0, ontology.Change{Record: topic}))
	require.NoError(t, err)
	config := maintenanceSettings(t, f)
	service, _ := maintenanceServiceFixture(t, f, config, nil)
	drainMaintenance(t, service)
	viewPage, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
	require.NoError(t, err)
	var dependent string
	for _, record := range viewPage.Records {
		if record.Assignment.Source.ID == b.ID {
			dependent = record.ID
			require.True(t, record.Current)
		}
	}
	require.NotEmpty(t, dependent)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{a.ID}, Reason: "withdraw definition support", IdempotencyKey: "withdraw-definition", RequestHash: testHash("withdraw-definition")})
	require.NoError(t, err)
	window := maintenanceWindow(t, f, config)
	turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	for range 10 {
		require.NoError(t, f.store.DiscoverMaintenance(context.Background(), *turn, ontology.MaintenancePageSize))
	}
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.Greater(t, status.Counts.Pending, int64(0))
	require.False(t, status.CoverageComplete)
	var state string
	require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind='evidence' AND source_id=?`, f.team, b.ID).Row().Scan(&state))
	require.Equal(t, "pending", state)
	view, err := f.store.GetRecord(context.Background(), f.team, dependent, 0)
	require.NoError(t, err)
	require.False(t, view.Current)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
}

func TestOntologyMaintenanceControlInterruptionResumesSameWindow(t *testing.T) {
	for _, action := range []string{"pause", "disable"} {
		t.Run(action, func(t *testing.T) {
			f := newOrganizationFixture(t)
			f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
			config := maintenanceSettings(t, f)
			window := maintenanceWindow(t, f, config)
			turn, claim := maintenanceClaim(t, f, window)
			ctx := context.Background()
			failure := "maintenance_paused"
			if action == "pause" {
				_, err := f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: "before-admission"}, time.Now().UTC())
				require.NoError(t, err)
			} else {
				_, err := config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
				require.NoError(t, err)
				failure = "maintenance_disabled"
			}
			err := f.store.ReserveMaintenanceAttempt(ctx, *claim, uuid.NewString(), 1, 100, 100, time.Now().UTC())
			require.Error(t, err)
			require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, failure, time.Now().UTC()))
			require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
			status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
			require.NoError(t, err)
			require.EqualValues(t, 1, status.Counts.Pending)
			require.Zero(t, status.Window.ChargedInput)
			if action == "pause" {
				_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{Action: "resume", OperationKey: "resume-same-window"}, time.Now().UTC())
			} else {
				_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "true"}, "control", "", "")
			}
			require.NoError(t, err)
			service, calls := maintenanceServiceFixture(t, f, config, nil)
			drainMaintenance(t, service)
			status, err = service.Status(ctx)
			require.NoError(t, err)
			require.Equal(t, window.ID, status.Window.ID)
			require.EqualValues(t, 1, status.Counts.Organized)
			require.EqualValues(t, 1, calls())
		})
	}
}

func TestOntologyMaintenanceRetryDoesNotReleaseUnselectedFailures(t *testing.T) {
	f := newOrganizationFixture(t)
	for _, text := range []string{"alpha", "bravo", "charlie"} {
		f.organizationEvidence(t, 0, text, nil)
	}
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyInputTokens: "1000000000", domain.AppConfigOntologyOutputTokens: "1000000000"}, "control", "", "")
	require.NoError(t, err)
	window := maintenanceWindow(t, f, config)
	var originalRun string
	require.NoError(t, f.admin.Raw(`SELECT run_id::text FROM ontology_maintenance_runs WHERE window_id=?::uuid AND kind='scheduled'`, window.ID).Row().Scan(&originalRun))
	broken := true
	service, calls := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, response *assessment.Response) {
		if broken {
			response.Items = nil
		}
	})
	for range 30 {
		progress, runErr := service.RunTurn(context.Background())
		if runErr != nil {
			var failure *organization.OrganizationError
			require.ErrorAs(t, runErr, &failure)
			require.NotEmpty(t, failure.Code)
		}
		if !progress {
			break
		}
	}
	status, err := service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 3, status.Counts.Failed)
	beforeCalls := calls()
	broken = false
	_, err = f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "retry", OperationKey: "single-retry", RetryRunID: originalRun, MaxBatches: 1}, time.Now().UTC())
	require.NoError(t, err)
	drainMaintenance(t, service)
	status, err = service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Organized)
	require.EqualValues(t, 2, status.Counts.Failed)
	require.EqualValues(t, beforeCalls+1, calls())
	broken = true
	unrelated, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "retry", OperationKey: "unrelated-failed-retry", RetryRunID: originalRun, MaxBatches: 1}, time.Now().UTC())
	require.NoError(t, err)
	_, err = service.RunTurn(context.Background())
	var failure *organization.OrganizationError
	require.ErrorAs(t, err, &failure)
	broken = false
	retried, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{Action: "retry", OperationKey: "complete-selected-retry", RetryRunID: originalRun, MaxBatches: 100}, time.Now().UTC())
	require.NoError(t, err)
	drainMaintenance(t, service)
	status, err = service.Status(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 2, status.Counts.Organized)
	require.EqualValues(t, 1, status.Counts.Failed)
	require.False(t, status.CoverageComplete)
	var runStatus string
	require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_runs WHERE run_id=?::uuid`, retried.ID).Row().Scan(&runStatus))
	require.Equal(t, "completed", runStatus)
	var unrelatedFailures int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE last_run_id=?::uuid AND status='failed'`, unrelated.ID).Row().Scan(&unrelatedFailures))
	require.Equal(t, 1, unrelatedFailures)
	runs, err := f.store.ListMaintenanceRuns(context.Background(), "", 100)
	require.NoError(t, err)
	var retryableIDs []string
	for _, run := range runs.Runs {
		if run.Retryable {
			retryableIDs = append(retryableIDs, run.ID)
		}
	}
	require.Equal(t, []string{unrelated.ID}, retryableIDs)
	for range 8 {
		_, err := service.RunTurn(context.Background())
		require.NoError(t, err)
	}
	require.EqualValues(t, beforeCalls+5, calls())
}

func TestOntologyMaintenanceCompetingAdmissionAndCrashRecovery(t *testing.T) {
	f := newOrganizationFixture(t)
	other := maintenanceOtherTeam(t, f)
	f.organizationEvidence(t, 0, "Atlas storage.", nil)
	other.organizationEvidence(t, 0, "Beacon storage.", nil)
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyConcurrency: "2", domain.AppConfigOntologyInputTokens: "100", domain.AppConfigOntologyOutputTokens: "100"}, "control", "", "")
	require.NoError(t, err)
	window := maintenanceWindow(t, f, config)
	_, a := maintenanceClaim(t, f, window)
	_, b := maintenanceClaim(t, f, window)
	require.NotEqual(t, a.TeamID, b.TeamID)
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, claim := range []*ontology.MaintenanceClaim{a, b} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- NewStore(f.app, f.rls).ReserveMaintenanceAttempt(context.Background(), *claim, uuid.NewString(), 1, 80, 80, time.Now().UTC())
		}()
	}
	workers.Wait()
	close(results)
	admitted, deferred := 0, 0
	for err := range results {
		if err == nil {
			admitted++
		} else {
			require.ErrorIs(t, err, ontology.ErrBudgetDeferred)
			deferred++
		}
	}
	require.Equal(t, 1, admitted)
	require.Equal(t, 1, deferred)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.app, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE ontology_maintenance_teams SET lease_until=clock_timestamp()-interval '1 second' WHERE lease_token IS NOT NULL`).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE status='running'`).Error
	}))
	restarted := NewStore(f.app, f.rls)
	next, err := restarted.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, next)
	status, err := restarted.MaintenanceStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 80, status.Window.ChargedInput)
	require.EqualValues(t, 80, status.Window.ReservedOutput)
	var lost int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE status='lost'`).Row().Scan(&lost))
	require.Equal(t, 2, lost)
	claim, err := restarted.ClaimMaintenanceBatch(context.Background(), *next, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.ErrorIs(t, restarted.ReserveMaintenanceAttempt(context.Background(), *claim, uuid.NewString(), 1, 30, 30, time.Now().UTC()), ontology.ErrBudgetDeferred)
	require.NoError(t, restarted.ReleaseMaintenanceTurn(context.Background(), *next))
}

func TestOntologyMaintenanceClaimRefreshesCurrentVersion(t *testing.T) {
	f := newOrganizationFixture(t)
	source := maintenanceEntity(t, f, "Atlas")
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	turn, claim := maintenanceClaim(t, f, window)
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE entity_records SET version=version+1,identity_context='{"new":true}' WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, source.ID).Error; err != nil {
			return err
		}
		return collectMaintenanceRevisions(tx, scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}, claim)
	}))
	require.EqualValues(t, source.Version+1, claim.Sources[0].Version)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
}

func assertNativeMaintenanceSeek(t *testing.T, f *ontologyFixture, cursor string) {
	t.Helper()
	var plan []byte
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL enable_seqscan=off`).Error; err != nil {
			return err
		}
		return tx.Raw(`EXPLAIN(ANALYZE,FORMAT JSON) `+maintenanceEntityPageSQL, f.team, f.space, f.generation, cursor, 101).Row().Scan(&plan)
	}))
	require.Contains(t, string(plan), "Index Cond")
	require.Contains(t, string(plan), "entity_id >")
	require.NotContains(t, string(plan), "((entity_id)::text)")
	require.False(t, strings.Contains(string(plan), "Seq Scan"))
}
