//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/domain"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypg "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type observedTopicTurns struct {
	communityapp.Service
	calls     atomic.Int32
	stopped   chan struct{}
	afterTurn func(context.Context, bool, error)
}

func (observer *observedTopicTurns) RunProjectionTurn(ctx context.Context) (bool, error) {
	observer.calls.Add(1)
	progress, err := observer.Service.RunProjectionTurn(ctx)
	if observer.afterTurn != nil {
		observer.afterTurn(ctx, progress, err)
	}
	if !progress || err != nil {
		select {
		case observer.stopped <- struct{}{}:
		default:
		}
	}
	return progress, err
}

func startObservedTopicScheduler(t *testing.T, f *topicProjectionFixture, now time.Time, afterTurn ...func(context.Context, bool, error)) (*observedTopicTurns, func()) {
	t.Helper()
	config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.appDB, f.rls), nil)
	policy, err := config.OntologyMaintenanceRuntimeConfig(context.Background())
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE ontology_maintenance_windows SET charged_input=?,charged_output=? WHERE window_id=?::uuid`, policy.InputTokens, policy.OutputTokens, f.windowID).Error
	}))
	real := communityapp.New(communityapp.Dependencies{Store: f.store, AppConfig: config, Now: func() time.Time { return now }})
	observer := &observedTopicTurns{Service: real, stopped: make(chan struct{}, policy.MaxConcurrency)}
	if len(afterTurn) > 0 {
		observer.afterTurn = afterTurn[0]
	}
	maintenance := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.ontology, Config: config, Projections: observer, Now: func() time.Time { return now },
		Organizer: func(model string, accounting assessment.AttemptAccounting) *organization.Service {
			return organization.NewService(f.ontology, assessment.NewProviderWithAccounting(nil, model, assessor.DefaultSemanticAssessmentLimits(), accounting))
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("topic scheduler did not stop before fixture cleanup")
		}
	}
	t.Cleanup(stop)
	go func() { defer close(done); organization.NewMaintenanceScheduler(maintenance, nil).Start(ctx) }()
	return observer, stop
}

func TestCommunityTopicSchedulerContinuesAfterAllCurrentCatalogPage(t *testing.T) {
	for _, later := range []bool{false, true} {
		t.Run(fmt.Sprintf("later-topic-%t", later), func(t *testing.T) {
			f := newTopicProjectionFixture(t)
			primeCurrentTopicCatalogPage(t, f)
			if !later {
				f.drain(t, 110)
			}
			observer, stop := startObservedTopicScheduler(t, f, time.Now())
			select {
			case <-observer.stopped:
			case <-time.After(2 * time.Minute):
				t.Fatal("catalog traversal did not terminate within one scheduler run")
			}
			stop()
			require.Len(t, f.currentIDs(t), 101)
			require.GreaterOrEqual(t, observer.calls.Load(), int32(2))
			require.LessOrEqual(t, observer.calls.Load(), int32(20))
			coverage, err := f.store.TopicProjectionCoverage(f.actor(f.reader), f.teamID)
			require.NoError(t, err)
			require.Equal(t, 101, coverage.CurrentTopics)
			require.Zero(t, coverage.PendingTopics+coverage.StaleTopics+coverage.FailedTopics)
		})
	}
}

func TestCommunityTopicSchedulerRotatesBusyTeamsAndStopsAtCapacityBounds(t *testing.T) {
	for _, mode := range []string{"available", "all-refused", "full", "paused"} {
		t.Run(mode, func(t *testing.T) {
			f := newTopicProjectionFixture(t)
			ctx := context.Background()
			for index := 0; index < 3; index++ {
				f.topic(t, fmt.Sprintf("available-%d", index), []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}})
			}
			changes := []ontology.Change{}
			for index := 0; index < 3; index++ {
				changes = append(changes, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: fmt.Sprintf("busy-%d", index), Label: fmt.Sprintf("Busy team %d", index)}}})
			}
			_, err := f.ontology.PublishAutomatic(ctx, f.otherTeam, ontology.Publication{OperationKey: uuid.NewString(), Reason: "busy team fixture", Changes: changes})
			require.NoError(t, err)
			config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.appDB, f.rls), nil)
			concurrency := "2"
			if mode == "all-refused" {
				concurrency = "3"
			}
			_, err = config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyConcurrency: concurrency}, "control", "", "")
			require.NoError(t, err)
			policy, err := config.OntologyMaintenanceRuntimeConfig(ctx)
			require.NoError(t, err)
			window, err := f.ontology.EnsureMaintenanceWindow(ctx, policy, time.Now())
			require.NoError(t, err)
			now := window.EndsAt.Add(time.Minute)
			window, err = f.ontology.EnsureMaintenanceWindow(ctx, policy, now)
			require.NoError(t, err)
			f.windowID = window.ID
			turn, err := f.ontology.ClaimMaintenanceTurn(ctx, window.ID, now, 15*time.Minute)
			require.NoError(t, err)
			require.NotNil(t, turn)
			if turn.TeamID == f.teamID {
				require.NoError(t, f.ontology.ReleaseMaintenanceTurn(ctx, *turn))
				turn, err = f.ontology.ClaimMaintenanceTurn(ctx, window.ID, now, 15*time.Minute)
				require.NoError(t, err)
				require.NotNil(t, turn)
			}
			require.Equal(t, f.otherTeam, turn.TeamID)
			t.Cleanup(func() { require.NoError(t, f.ontology.ReleaseMaintenanceTurn(ctx, *turn)) })
			if mode == "all-refused" || mode == "full" {
				second, err := f.ontology.ClaimMaintenanceTurn(ctx, window.ID, now, 15*time.Minute)
				require.NoError(t, err)
				require.NotNil(t, second)
				t.Cleanup(func() { require.NoError(t, f.ontology.ReleaseMaintenanceTurn(ctx, *second)) })
			}
			require.NoError(t, f.store.withProjectionTx(ctx, func(tx *gorm.DB) error {
				if _, err := f.store.discoverTopicProjections(ctx, tx); err != nil {
					return err
				}
				return tx.Exec(`UPDATE community_topic_work SET last_turn=CASE WHEN team_id=?::uuid THEN ?::timestamptz ELSE ?::timestamptz END`, f.otherTeam, now.Add(-2*time.Minute), now.Add(-time.Minute)).Error
			}))
			if mode == "paused" {
				require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
					return tx.Exec(`UPDATE ontology_maintenance_state SET paused=true WHERE singleton`).Error
				}))
			}
			observer, stop := startObservedTopicScheduler(t, f, now)
			switch mode {
			case "available":
				require.Eventually(t, func() bool { return len(f.currentIDs(t)) == 3 }, 2*time.Minute, 20*time.Millisecond)
			case "all-refused":
				require.Eventually(t, func() bool { return observer.calls.Load() == int32(20*policy.MaxConcurrency) }, 20*time.Second, 20*time.Millisecond)
			case "full":
				select {
				case <-observer.stopped:
				case <-time.After(10 * time.Second):
					t.Fatal("full queue kept spinning")
				}
			case "paused":
				time.Sleep(100 * time.Millisecond)
			}
			stop()
			require.LessOrEqual(t, observer.calls.Load(), int32(20*policy.MaxConcurrency))
			if mode == "full" {
				require.LessOrEqual(t, observer.calls.Load(), int32(policy.MaxConcurrency))
			}
			if mode == "paused" {
				require.Zero(t, observer.calls.Load())
			}
			if mode != "available" {
				require.Empty(t, f.currentIDs(t))
			}
		})
	}
}

func TestCommunityTopicSchedulerEarlyAssertionCleanup(t *testing.T) {
	if os.Getenv("DENSE_MEM_TOPIC_EARLY_EXIT_CHILD") == "1" {
		f := newTopicProjectionFixture(t)
		_, stop := startObservedTopicScheduler(t, f, time.Now())
		t.Cleanup(func() { stop(); fmt.Println("topic scheduler stopped before fixture cleanup") })
		t.Fatal("deliberate early assertion failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommunityTopicSchedulerEarlyAssertionCleanup$", "-test.v")
	command.Env = append(os.Environ(), "DENSE_MEM_TOPIC_EARLY_EXIT_CHILD=1")
	output, err := command.CombinedOutput()
	require.Error(t, err)
	require.NoError(t, ctx.Err())
	require.Contains(t, string(output), "deliberate early assertion failure")
	require.Contains(t, string(output), "topic scheduler stopped before fixture cleanup")
}

func primeCurrentTopicCatalogPage(t *testing.T, f *topicProjectionFixture) {
	t.Helper()
	changes := []ontology.Change{}
	for index := 1; index <= 101; index++ {
		id := fmt.Sprintf("60000000-0000-4000-8000-%012d", index)
		key := fmt.Sprintf("catalog-%03d", index)
		f.topics[key] = id
		changes = append(changes, ontology.Change{Record: ontology.Record{ID: id, Kind: ontology.Topic, Definition: &ontology.Definition{Key: key, Label: key}}})
	}
	for offset := 0; offset < 100; offset += 64 {
		f.publish(t, changes[offset:min(offset+64, 100)]...)
	}
	for index := 1; index <= 100; index++ {
		f.assignTopic(t, f.topics[fmt.Sprintf("catalog-%03d", index)], []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}})
	}
	f.drain(t, 110)
	require.Len(t, f.currentIDs(t), 100)
	f.publish(t, changes[100])
	f.assignTopic(t, f.topics["catalog-101"], []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[1].RelationshipID, Version: int64(f.sources[1].RelationshipVersion)}})
}

func TestCommunityTopicSchedulerCancellationRestartsPersistedDiscovery(t *testing.T) {
	f := newTopicProjectionFixture(t)
	primeCurrentTopicCatalogPage(t, f)
	original := f.currentIDs(t)
	canonical := f.canonicalSnapshot(t)
	committed := make(chan struct{})
	var once atomic.Bool
	observer, stop := startObservedTopicScheduler(t, f, time.Now(), func(ctx context.Context, progress bool, err error) {
		if progress && err == nil && once.CompareAndSwap(false, true) {
			close(committed)
			<-ctx.Done()
		}
	})
	select {
	case <-committed:
	case <-time.After(20 * time.Second):
		t.Fatal("scheduler did not commit its first discovery page")
	}
	cursor := func() ontology.TopicCatalogCursor {
		var result ontology.TopicCatalogCursor
		require.NoError(t, f.adminDB.Raw(`SELECT after_team,after_topic FROM community_topic_discovery WHERE singleton`).Row().Scan(&result.TeamID, &result.TopicID))
		return result
	}
	saved := cursor()
	require.Equal(t, f.teamID, saved.TeamID)
	require.Equal(t, f.topics["catalog-100"], saved.TopicID)
	stop()
	require.LessOrEqual(t, observer.calls.Load(), int32(2))
	require.Equal(t, saved, cursor())
	require.Equal(t, original, f.currentIDs(t))
	assertReleased := func() {
		var leases int
		require.NoError(t, f.adminDB.Raw(`SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>now()`).Row().Scan(&leases))
		require.Zero(t, leases, "cancelled scheduler retained a team lease")
	}
	assertReleased()
	f.ontology = ontologypg.NewStore(f.appDB.Session(&gorm.Session{NewDB: true}), f.rls)
	f.store = NewStore(f.appDB.Session(&gorm.Session{NewDB: true}), f.rls).WithTopics(f.ontology.NewTopicCatalogReader(), f.ontology.NewTopicMembershipReader(), f.ontology.NewTopicProjectionAdmission(), f.ontology.NewTopicProjectionRelease())
	restarted, join := startObservedTopicScheduler(t, f, time.Now())
	select {
	case <-restarted.stopped:
	case <-time.After(2 * time.Minute):
		t.Fatal("restarted scheduler did not finish persisted discovery")
	}
	join()
	require.LessOrEqual(t, restarted.calls.Load(), int32(20))
	ids := f.currentIDs(t)
	require.Len(t, ids, 101)
	for topic, id := range original {
		require.Equal(t, id, ids[topic], "restart rebuilt an already-current projection")
	}
	require.Equal(t, canonical, f.canonicalSnapshot(t))
	coverage, err := f.store.TopicProjectionCoverage(f.actor(f.reader), f.teamID)
	require.NoError(t, err)
	require.Equal(t, 101, coverage.CurrentTopics)
	require.Zero(t, coverage.PendingTopics+coverage.StaleTopics+coverage.FailedTopics)
	var sources []string
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT relationship_id::text FROM community_sources WHERE community_id=?::uuid`, ids[f.topics["catalog-101"]]).Scan(&sources).Error
	}))
	require.Equal(t, []string{f.sources[1].RelationshipID}, sources)
	assertReleased()
}
