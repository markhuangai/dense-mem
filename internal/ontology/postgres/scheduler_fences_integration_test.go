//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyMaintenanceParallelSchedulerPublicationFences(t *testing.T) {
	for _, race := range []string{"dependency", "claim"} {
		t.Run(race, func(t *testing.T) {
			f := newOrganizationFixture(t)
			topic := schedulerTopic(t, f)
			concurrencySources(t, f)
			config := schedulerConfig(t, f, 2, 2)
			canonical := schedulerCanonical(t, f)
			providerGate, projectionGate := make(chan struct{}), make(chan struct{})
			var providerOnce, projectionOnce, firstOnce sync.Once
			releaseProjection := func() { projectionOnce.Do(func() { close(projectionGate) }) }
			release := func() { providerOnce.Do(func() { close(providerGate) }); releaseProjection() }
			firstProvider := make(chan struct{})
			service, _ := schedulerFixtureService(t, f, config, func(_ assessment.Request, _ *assessment.Response) {
				firstOnce.Do(func() { close(firstProvider) })
				<-providerGate
			})
			entered := make(chan community.TopicProjectionBatch, 100)
			answers := make(chan error, 100)
			projections, store := schedulerProjections(f, config, func(ctx context.Context, batch community.TopicProjectionBatch) error {
				select {
				case entered <- batch:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case <-projectionGate:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, func(err error) { answers <- err })
			projections.before = func(ctx context.Context) {
				select {
				case <-firstProvider:
				case <-ctx.Done():
				}
			}
			service.SetProjectionRunner(projections)
			_, stop := startScheduler(t, service, release, 1)
			defer stop()
			var batch community.TopicProjectionBatch
			select {
			case batch = <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("projection did not reach publication while organization was in flight")
			}
			var replacement *community.TopicProjectionWork
			if race == "dependency" {
				view, err := f.store.GetRecord(f.actor(0, "manager"), f.team, topic.ID, 0)
				require.NoError(t, err)
				page, err := f.store.ListRecords(f.actor(0, "manager"), f.team, "", "", 1)
				require.NoError(t, err)
				view.Definition.Label = "Changed during the projection turn"
				_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), page.Revision, ontology.Change{Record: view.Record, ExpectedVersion: view.Version}))
				require.NoError(t, err)
			} else {
				require.NoError(t, f.admin.Exec(`UPDATE community_topic_work SET lease_until=clock_timestamp()-interval '1 second' WHERE topic_id=?::uuid`, topic.ID).Error)
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_teams SET lease_until=clock_timestamp()-interval '1 second' WHERE team_id=?::uuid`, f.team).Error)
				var err error
				replacement, _, err = store.ClaimTopicProjection(context.Background(), time.Now())
				require.NoError(t, err)
				require.NotNil(t, replacement)
				require.NotEqual(t, batch.Work.LeaseToken, replacement.LeaseToken)
			}
			releaseProjection()
			select {
			case err := <-answers:
				if race == "dependency" {
					require.ErrorIs(t, err, community.ErrCommunitySourceStale)
				} else {
					require.ErrorIs(t, err, community.ErrCommunityRunAlreadyClaimed)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("stale publication did not terminate")
			}
			stop()
			if replacement != nil {
				var token string
				require.NoError(t, f.admin.Raw(`SELECT lease_token::text FROM community_topic_work WHERE topic_id=?::uuid`, topic.ID).Row().Scan(&token))
				require.Equal(t, replacement.LeaseToken, token, "stale worker changed replacement ownership")
				require.NoError(t, store.FailTopicProjection(context.Background(), *replacement, "interrupted", time.Now()))
			}
			schedulerAssertDrained(t, f)
			require.Equal(t, canonical, schedulerCanonical(t, f))
		})
	}
}

func TestOntologyMaintenanceParallelSchedulerUnavailableCapacity(t *testing.T) {
	f := newOrganizationFixture(t)
	schedulerTopic(t, f)
	other := maintenanceOtherTeam(t, f)
	other.organizationEvidence(t, 0, "Other team's pending source.", nil)
	config := schedulerConfig(t, f, 2, 1)
	window := maintenanceWindow(t, f, config)
	var turns []ontology.MaintenanceTurn
	for range 2 {
		turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now(), time.Minute)
		require.NoError(t, err)
		require.NotNil(t, turn)
		turns = append(turns, *turn)
	}
	defer func() {
		for _, turn := range turns {
			require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), turn))
		}
	}()
	service, calls := schedulerFixtureService(t, f, config, nil)
	projections, _ := schedulerProjections(f, config, nil, nil)
	service.SetProjectionRunner(projections)
	_, stop := startScheduler(t, service, nil, 1)
	defer stop()
	require.Eventually(t, func() bool {
		projections.mu.Lock()
		defer projections.mu.Unlock()
		return len(projections.turns) > 0
	}, 10*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	stop()
	require.Zero(t, calls())
	projections.mu.Lock()
	count := len(projections.turns)
	projections.mu.Unlock()
	require.LessOrEqual(t, count, 2, "unavailable capacity must not spin")
}
