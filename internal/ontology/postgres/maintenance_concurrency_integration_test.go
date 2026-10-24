//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func concurrencyConfig(t *testing.T, f *ontologyFixture, global, team int) *settings.AppConfigServiceImpl {
	t.Helper()
	config := maintenanceSettings(t, f)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{
		domain.AppConfigOntologyConcurrency: fmt.Sprint(global), domain.AppConfigOntologyTeamConcurrency: fmt.Sprint(team),
	}, "control", "", "")
	require.NoError(t, err)
	return config
}

func concurrencySources(t *testing.T, f *ontologyFixture) {
	t.Helper()
	for i := range 25 {
		f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("Parallel cohort shared sentence %d.", i), nil)
	}
}

func assertDisjointClaims(t *testing.T, claims ...*ontology.MaintenanceClaim) {
	t.Helper()
	seen := map[string]bool{}
	for _, claim := range claims {
		for _, source := range claim.Sources {
			key := ontology.SourceKey(source)
			require.False(t, seen[key], "source owned by two live batches: %s", key)
			seen[key] = true
		}
	}
}

func TestOntologyMaintenanceProgressBeyondFormerBudgets(t *testing.T) {
	started := time.Now()
	f := newOrganizationFixture(t)
	teams := []*ontologyFixture{f, maintenanceOtherTeam(t, f), maintenanceOtherTeam(t, f)}
	for index, team := range teams {
		for i := range 60 {
			team.organizationEvidence(t, i%len(team.owners), fmt.Sprintf("Independent backlog %d sample %d.", index, i), nil)
		}
	}
	config := concurrencyConfig(t, f, 8, 3)
	window := maintenanceWindow(t, f, config)
	before := map[string]map[string]string{}
	for _, team := range teams {
		before[team.team] = team.canonicalSnapshot(t)
	}
	ctx := context.Background()
	var claims []*ontology.MaintenanceClaim
	var ids []string
	seen := map[string]bool{}
	for range 8 {
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), 3*time.Minute)
		require.NoError(t, err)
		require.NotNil(t, turn)
		for range 8 {
			require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
		}
		claim, err := f.store.ClaimMaintenanceBatch(ctx, *turn, window.ID, time.Now())
		require.NoError(t, err)
		require.NotNil(t, claim)
		id := uuid.NewString()
		require.NoError(t, NewStore(f.app, f.rls).AdmitMaintenanceAttempt(ctx, *claim, id, 1, 50000, 65536, time.Now()))
		claims = append(claims, claim)
		ids = append(ids, id)
		seen[claim.TeamID] = true
	}
	require.Len(t, seen, 3)
	assertDisjointClaims(t, claims...)
	blocked, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, blocked)
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, 8, status.ActiveWork)
	require.EqualValues(t, 8, status.Usage.UnreconciledAttempts)
	var outputCapacity int64
	require.NoError(t, f.admin.Raw(`SELECT sum(max_output_tokens) FROM ontology_maintenance_attempts WHERE window_id=?::uuid`, window.ID).Row().Scan(&outputCapacity))
	require.Greater(t, outputCapacity, int64(500000), "per-call response capacity is not an aggregate allowance")
	for i, claim := range claims {
		attempt := ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 40000, ReportedOutputTokens: 20000, ReportedTotalTokens: 60000}
		if i == 0 {
			attempt = ontology.AssessmentAttempt{Number: 1}
		}
		require.NoError(t, f.store.ReconcileMaintenanceAttempt(ctx, *claim, ids[i], attempt))
		require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "maintenance_paused", time.Now()))
	}
	status, err = f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.Greater(t, status.Usage.ReportedInputTokens, int64(250000))
	require.Greater(t, status.Usage.ReportedOutputTokens, int64(100000))
	require.EqualValues(t, 1, status.Usage.UnavailableUsageAttempts)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.Zero(t, status.ActiveWork)
	for _, team := range teams {
		require.Equal(t, before[team.team], team.canonicalSnapshot(t))
	}
	next, err := NewStore(f.app, f.rls).ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, next, "reported totals and completed unknown usage must not stop future work")
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *next))
	t.Logf("teams=%d admitted_attempts=%d reported_input=%d reported_output=%d unavailable_usage=%d elapsed=%s", len(seen), len(claims), status.Usage.ReportedInputTokens, status.Usage.ReportedOutputTokens, status.Usage.UnavailableUsageAttempts, time.Since(started))
}

func TestOntologyMaintenanceParallelClaimsAndLimits(t *testing.T) {
	f := newOrganizationFixture(t)
	concurrencySources(t, f)
	config := concurrencyConfig(t, f, 3, 2)
	window := maintenanceWindow(t, f, config)
	ctx := context.Background()
	_, first := maintenanceClaim(t, f, window)
	_, second := maintenanceClaim(t, f, window)
	assertDisjointClaims(t, first, second)
	require.LessOrEqual(t, len(first.Sources), ontology.MaxOrganizationSources)
	blocked, err := NewStore(f.app, f.rls).ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, blocked, "the team cap applies across store instances")
	projection := ontology.MaintenanceTurn{TeamID: f.team, SpaceID: f.space, Generation: f.generation, LeaseToken: uuid.NewString(), LeaseUntil: time.Now().Add(time.Minute)}
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		admission, err := f.store.NewTopicProjectionAdmission()(ctx, tx, projection, time.Now())
		require.Equal(t, ontology.TopicProjectionTeamBusy, admission)
		return err
	}))
	other := maintenanceOtherTeam(t, f)
	other.organizationEvidence(t, 0, "Foreign isolated cohort.", nil)
	turn, err := NewStore(f.app, f.rls).ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	require.Equal(t, other.team, turn.TeamID, "fair rotation admits a competing team")
	blocked, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, blocked, "preparation and batch slots share the global cap")
	var leaked int
	require.NoError(t, f.rls.WithTeamTx(other.actor(0, "member"), f.app, other.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE team_id=?::uuid`, f.team).Row().Scan(&leaked)
	}))
	require.Zero(t, leaked)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *first, ontology.OrganizationResult{}, "maintenance_paused", time.Now()))
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		admission, err := f.store.NewTopicProjectionAdmission()(ctx, tx, projection, time.Now())
		require.Equal(t, ontology.TopicProjectionAdmitted, admission)
		return err
	}))
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error { return f.store.NewTopicProjectionRelease()(ctx, tx, projection) }))
}

func TestOntologyMaintenanceParallelWindowPolicy(t *testing.T) {
	f := newOrganizationFixture(t)
	config := concurrencyConfig(t, f, 4, 1)
	window := maintenanceWindow(t, f, config)
	ctx := context.Background()
	var rawBefore string
	require.NoError(t, f.admin.Raw(`SELECT policy::text FROM ontology_maintenance_windows WHERE window_id=?::uuid`, window.ID).Row().Scan(&rawBefore))
	overrides, err := json.Marshal([]domain.OntologyTeamConcurrencyOverride{{TeamID: f.team, MaxConcurrency: 8}})
	require.NoError(t, err)
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyTeamConcurrency: "2", domain.AppConfigOntologyTeamOverrides: string(overrides)}, "control", "", "")
	require.NoError(t, err)
	policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	same, err := f.store.EnsureMaintenanceWindow(ctx, policy, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, same.Policy.TeamConcurrency(f.team))
	var rawAfter string
	require.NoError(t, f.admin.Raw(`SELECT policy::text FROM ontology_maintenance_windows WHERE window_id=?::uuid`, window.ID).Row().Scan(&rawAfter))
	require.Equal(t, rawBefore, rawAfter)
	next, err := f.store.EnsureMaintenanceWindow(ctx, policy, window.EndsAt)
	require.NoError(t, err)
	require.Equal(t, 4, next.Policy.TeamConcurrency(f.team))
	require.Equal(t, 2, next.Policy.TeamConcurrency(uuid.NewString()))
	_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyTeamOverrides: "[]"}, "control", "", "")
	require.NoError(t, err)
	policy, err = config.OntologyMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	last, err := f.store.EnsureMaintenanceWindow(ctx, policy, next.EndsAt)
	require.NoError(t, err)
	require.Equal(t, 2, last.Policy.TeamConcurrency(f.team))
}

func TestOntologyMaintenanceParallelIndependentPublication(t *testing.T) {
	f := newOrganizationFixture(t)
	sources := []ontology.SourceHandle{f.organizationEvidence(t, 0, "Alphaquartz", nil), f.organizationEvidence(t, 1, "Betacobalt", nil)}
	before := f.canonicalSnapshot(t)
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	service, _ := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
		for i := range response.Definitions {
			response.Definitions[i].Key = strings.ToLower(request.Items[0].Text)
			response.Definitions[i].Label = request.Items[0].Text
			response.Definitions[i].Description = request.Items[0].Text
		}
		arrived <- struct{}{}
		<-release
	})
	type answer struct {
		result ontology.OrganizationResult
		err    error
	}
	answers := make(chan answer, 2)
	for _, source := range sources {
		go func() {
			result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: source.ID, Sources: []ontology.SourceHandle{source}})
			answers <- answer{result, err}
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			finish()
			t.Fatal("publication race did not reach both provider calls")
		}
	}
	finish()
	for range 2 {
		answer := <-answers
		require.NoError(t, answer.err)
		require.True(t, answer.result.Current)
		require.NotNil(t, answer.result.Publication)
	}
	require.Equal(t, 2, receiptCount(t, f))
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceParallelDefaultAndManualBound(t *testing.T) {
	f := newOrganizationFixture(t)
	concurrencySources(t, f)
	config := concurrencyConfig(t, f, 3, 1)
	window := maintenanceWindow(t, f, config)
	ctx := context.Background()
	command, err := f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{OperationKey: uuid.NewString(), Action: "run", MaxBatches: 1}, time.Now(), nil)
	require.NoError(t, err)
	_, first := maintenanceClaim(t, f, window)
	require.Equal(t, command.ID, first.RunID)
	blocked, err := NewStore(f.app, f.rls).ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.Nil(t, blocked, "global concurrency alone must preserve serial team behavior")
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *first, ontology.OrganizationResult{}, "maintenance_paused", time.Now()))
	_, second := maintenanceClaim(t, f, window)
	require.NotEqual(t, command.ID, second.RunID)
	var manualBatches int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE run_id=?::uuid`, command.ID).Row().Scan(&manualBatches))
	require.Equal(t, 1, manualBatches)
}

func TestOntologyMaintenanceParallelClosureAndLeaseRecovery(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Alphaquartz", nil)
	config := concurrencyConfig(t, f, 3, 3)
	window := maintenanceWindow(t, f, config)
	ctx := context.Background()
	_, first := maintenanceClaim(t, f, window)
	require.Equal(t, []ontology.SourceHandle{a}, first.Sources)
	b := f.organizationEvidence(t, 0, "Alphaquartz", nil)
	c := f.organizationEvidence(t, 0, "Gammacitrine", nil)
	group := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.GroupTogether, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("group-inflight", 0, ontology.Change{Record: group}))
	require.NoError(t, err)
	_, second := maintenanceClaim(t, f, window)
	require.Equal(t, []ontology.SourceHandle{c}, second.Sources, "a busy closure is deferred whole, without hiding independent work")
	before := f.canonicalSnapshot(t)
	require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, first.ID).Error)
	_, reclaimed := maintenanceClaim(t, f, window)
	assertDisjointClaims(t, second, reclaimed)
	require.ElementsMatch(t, []ontology.SourceHandle{a, b}, reclaimed.Sources)
	require.ErrorIs(t, f.store.CompleteMaintenanceBatch(ctx, *first, ontology.OrganizationResult{}, "", time.Now()), ontology.ErrLeaseLost)
	require.ErrorIs(t, f.store.AdmitMaintenanceAttempt(ctx, *first, uuid.NewString(), 1, 1, 1, time.Now()), ontology.ErrLeaseLost)
	var status string
	require.NoError(t, f.admin.Raw(`SELECT status FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, first.ID).Row().Scan(&status))
	require.Equal(t, "lost", status)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceParallelTokenAdmissionAndDrain(t *testing.T) {
	f := newOrganizationFixture(t)
	concurrencySources(t, f)
	config := concurrencyConfig(t, f, 2, 2)
	var err error
	window := maintenanceWindow(t, f, config)
	_, a := maintenanceClaim(t, f, window)
	_, b := maintenanceClaim(t, f, window)
	ctx := context.Background()
	ids := []string{uuid.NewString(), uuid.NewString()}
	claims := []*ontology.MaintenanceClaim{a, b}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = NewStore(f.app, f.rls).AdmitMaintenanceAttempt(ctx, *claims[i], ids[i], 1, 80, 80, time.Now())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 2, status.Usage.UnreconciledAttempts)

	for _, action := range []string{"pause", "disable"} {
		if action == "pause" {
			_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{OperationKey: uuid.NewString(), Action: "pause"}, time.Now(), nil)
		} else {
			_, err = f.store.MaintenanceCommand(ctx, domain.OntologyMaintenanceCommand{OperationKey: uuid.NewString(), Action: "resume"}, time.Now(), nil)
			require.NoError(t, err)
			_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
		}
		require.NoError(t, err)
		expected := ontology.ErrMaintenancePaused
		if action == "disable" {
			expected = ontology.ErrMaintenanceDisabled
		}
		require.ErrorIs(t, f.store.AdmitMaintenanceAttempt(ctx, *a, ids[0], 2, 1, 1, time.Now()), expected)
		_, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
		require.ErrorIs(t, err, expected)
	}
	before := f.canonicalSnapshot(t)
	for i, claim := range claims {
		require.NoError(t, f.store.ReconcileMaintenanceAttempt(ctx, *claim, ids[i], ontology.AssessmentAttempt{Number: 1, ReportedInputTokens: 60, ReportedOutputTokens: 30}))
		require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "maintenance_disabled", time.Now()))
	}
	status, err = f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 120, status.Window.Usage.ReportedInputTokens)
	require.EqualValues(t, 60, status.Window.Usage.ReportedOutputTokens)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.Zero(t, status.ActiveWork)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceParallelSchedulerExecution(t *testing.T) {
	f := newOrganizationFixture(t)
	concurrencySources(t, f)
	config := concurrencyConfig(t, f, 2, 2)
	arrived := make(chan struct{}, 100)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	t.Cleanup(finish)
	service, _ := maintenanceServiceFixture(t, f, config, func(_ assessment.Request, _ *assessment.Response) { arrived <- struct{}{}; <-release })
	before := f.canonicalSnapshot(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		finish()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("scheduler cleanup timed out")
		}
	})
	go func() { defer close(done); organization.NewMaintenanceScheduler(service, nil).Start(ctx) }()
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(20 * time.Second):
			t.Fatal("one process did not dispatch two simultaneous team batches")
		}
	}
	var active, leases int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE team_id=?::uuid AND status='running'`, f.team).Row().Scan(&active))
	require.Equal(t, 2, active)
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp()`).Row().Scan(&leases))
	require.Zero(t, leases, "capacity transfers to batch leases before provider I/O")
	finish()
	require.Eventually(t, func() bool {
		var active int
		err := f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'`).Row().Scan(&active)
		return err == nil && active == 0
	}, 20*time.Second, 20*time.Millisecond)
	cancel()
	<-done
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyMaintenanceParallelMigrationGuard(t *testing.T) {
	f := newOrganizationFixture(t)
	concurrencySources(t, f)
	config := concurrencyConfig(t, f, 2, 2)
	window := maintenanceWindow(t, f, config)
	_, a := maintenanceClaim(t, f, window)
	_, b := maintenanceClaim(t, f, window)
	name := "20261021010000_ontology_team_concurrency.sql"
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "postgres", "v2_6", name))
	require.NoError(t, err)
	db, err := f.admin.DB()
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{name: &fstest.MapFile{Data: source}}, goose.WithTableName("ontology_parallel_migration_probe"), goose.WithDisableGlobalRegistry(true))
	require.NoError(t, err)
	require.NoError(t, f.admin.Exec(`DROP INDEX ontology_maintenance_batch_team_idx`).Error)
	require.NoError(t, f.admin.Exec(`CREATE INDEX ontology_maintenance_batch_team_idx ON ontology_maintenance_batches(team_id) WHERE status='running'`).Error)
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
	_, err = provider.Down(context.Background())
	require.ErrorContains(t, err, "drain ontology work")
	for _, claim := range []*ontology.MaintenanceClaim{a, b} {
		require.NoError(t, f.store.CompleteMaintenanceBatch(context.Background(), *claim, ontology.OrganizationResult{}, "maintenance_paused", time.Now()))
	}
	_, err = provider.Down(context.Background())
	require.NoError(t, err)
	var unique bool
	require.NoError(t, f.admin.Raw(`SELECT indisunique FROM pg_index WHERE indexrelid='ontology_maintenance_batch_team_idx'::regclass`).Row().Scan(&unique))
	require.True(t, unique)
	_, err = provider.Up(context.Background())
	require.NoError(t, err)
}
