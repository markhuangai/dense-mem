//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	community "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceParallelSchedulerMixedTurns(t *testing.T) {
	for _, capacity := range []int{4, 8} {
		for _, teamCount := range []int{1, 2} {
			t.Run(fmt.Sprintf("capacity-%d-teams-%d", capacity, teamCount), func(t *testing.T) {
				f := newOrganizationFixture(t)
				teams := []*ontologyFixture{f}
				if teamCount == 2 {
					teams = append(teams, maintenanceOtherTeam(t, f))
				}
				for _, team := range teams {
					schedulerTopic(t, team)
					for i := range 25 {
						team.organizationEvidence(t, i%len(team.owners), fmt.Sprintf("Mixed scheduling source %d.", i), nil)
					}
				}
				config := schedulerConfig(t, f, capacity, capacity)
				m := &schedulerMeasurement{started: time.Now(), MaxTeamAdmitted: map[string]int{}}
				gate, firstProvider := make(chan struct{}), make(chan struct{})
				var releaseOnce, firstOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(gate) }) }
				entered := make(chan struct{}, 100)
				provider := schedulerProvider(m, f, entered, gate)
				service, _ := schedulerFixtureService(t, f, config, func(request assessment.Request, response *assessment.Response) {
					firstOnce.Do(func() { close(firstProvider) })
					provider(request, response)
				})
				projectionEntered := make(chan struct{}, 1)
				projections, _ := schedulerProjections(f, config, func(ctx context.Context, _ community.TopicProjectionBatch) error {
					m.mu.Lock()
					m.projections++
					m.MixedOverlap = m.MixedOverlap || m.providers > 0
					m.mu.Unlock()
					defer func() { m.mu.Lock(); m.projections--; m.mu.Unlock() }()
					m.sample(f)
					select {
					case projectionEntered <- struct{}{}:
					default:
					}
					select {
					case <-gate:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, nil)
				projections.before = func(ctx context.Context) {
					select {
					case <-firstProvider:
					case <-ctx.Done():
					}
				}
				service.SetProjectionRunner(projections)
				canonical := schedulerCanonical(t, teams...)
				_, stop := startScheduler(t, service, release, 2)
				defer stop()
				select {
				case <-projectionEntered:
				case <-time.After(30 * time.Second):
					t.Fatal("a real projection did not overlap admitted organization work")
				}
				m.mu.Lock()
				mixed, maxAdmitted := m.MixedOverlap, m.MaxAdmitted
				var peaks []int
				for _, peak := range m.MaxTeamAdmitted {
					peaks = append(peaks, peak)
				}
				errors := append([]string(nil), m.Errors...)
				m.mu.Unlock()
				require.True(t, mixed)
				require.LessOrEqual(t, maxAdmitted, capacity)
				for _, peak := range peaks {
					require.LessOrEqual(t, peak, capacity)
				}
				require.Empty(t, errors)
				release()
				require.EventuallyWithT(t, func(c *assert.CollectT) {
					var active, organizedTeams int
					err := f.admin.Raw(`SELECT
						(SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp())+
						(SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'),
						(SELECT count(DISTINCT team_id) FROM ontology_maintenance_sources WHERE status='organized')`).Row().Scan(&active, &organizedTeams)
					require.NoError(c, err)
					require.Zero(c, active)
					require.Equal(c, teamCount, organizedTeams)
				}, 30*time.Second, 20*time.Millisecond)
				stop()
				schedulerAssertDrained(t, f)
				require.Equal(t, canonical, schedulerCanonical(t, teams...))
				if teamCount == 2 {
					for _, team := range teams {
						var organized int
						require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, team.team).Row().Scan(&organized))
						require.Positive(t, organized, "both teams must make progress")
					}
					var foreign int
					require.NoError(t, f.rls.WithTeamProfileTx(teams[1].actor(0, "member"), f.app, teams[1].team, teams[1].owners[0], func(tx *gorm.DB) error {
						return tx.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE team_id=?::uuid`, f.team).Row().Scan(&foreign)
					}))
					require.Zero(t, foreign)
				}
			})
		}
	}
}

func TestOntologyMaintenanceParallelSchedulerFillsAvailableSlots(t *testing.T) {
	for _, limits := range [][2]int{{1, 1}, {4, 1}, {4, 4}, {8, 8}} {
		t.Run(fmt.Sprintf("global-%d-team-%d", limits[0], limits[1]), func(t *testing.T) {
			f := newOrganizationFixture(t)
			for i := range limits[1]*ontology.MaxOrganizationSources + 1 {
				f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("Slot fill source %d.", i), nil)
			}
			config := schedulerConfig(t, f, limits[0], limits[1])
			m := &schedulerMeasurement{started: time.Now(), MaxTeamAdmitted: map[string]int{}}
			gate, firstGate, entered := make(chan struct{}), make(chan struct{}), make(chan struct{}, 100)
			var once, firstOnce sync.Once
			releaseFirst := func() { firstOnce.Do(func() { close(firstGate) }) }
			release := func() { releaseFirst(); once.Do(func() { close(gate) }) }
			firstProvider := schedulerProvider(m, f, entered, firstGate)
			otherProvider := schedulerProvider(m, f, entered, gate)
			var dispatched atomic.Int32
			service, _ := schedulerFixtureService(t, f, config, func(request assessment.Request, response *assessment.Response) {
				if dispatched.Add(1) == 1 {
					firstProvider(request, response)
				} else {
					otherProvider(request, response)
				}
			})
			canonical := schedulerCanonical(t, f)
			_, stop := startScheduler(t, service, release, 1)
			defer stop()
			for range limits[1] {
				select {
				case <-entered:
				case <-time.After(30 * time.Second):
					t.Fatal("refused preparation did not retry after lease transfer")
				}
			}
			m.mu.Lock()
			overlap, maxAdmitted := m.MaxProviderOverlap, m.MaxAdmitted
			errors := append([]string(nil), m.Errors...)
			m.mu.Unlock()
			require.Equal(t, limits[1], overlap)
			require.LessOrEqual(t, maxAdmitted, limits[0])
			require.Empty(t, errors)
			if limits[1] > 1 {
				releaseFirst()
				select {
				case <-entered:
				case <-time.After(30 * time.Second):
					t.Fatal("released slot was not refilled while other providers remained blocked")
				}
				select {
				case <-gate:
					t.Fatal("the independent provider blockers were released before refill")
				default:
				}
				m.mu.Lock()
				providers, maxAdmitted := m.providers, m.MaxAdmitted
				errors := append([]string(nil), m.Errors...)
				m.mu.Unlock()
				require.Equal(t, limits[1], providers)
				require.LessOrEqual(t, maxAdmitted, limits[0])
				require.Empty(t, errors)
			}
			stop()
			schedulerAssertDrained(t, f)
			require.Equal(t, canonical, schedulerCanonical(t, f))
		})
	}
}

func TestOntologyMaintenanceParallelSchedulerDrainControls(t *testing.T) {
	for _, action := range []string{"pause", "disable", "cancel"} {
		t.Run(action, func(t *testing.T) {
			f := newOrganizationFixture(t)
			schedulerTopic(t, f)
			concurrencySources(t, f)
			config := schedulerConfig(t, f, 4, 4)
			providerGate, projectionGate := make(chan struct{}), make(chan struct{})
			firstProvider, projectionEntered := make(chan struct{}), make(chan struct{}, 1)
			var providerOnce, projectionOnce, firstOnce sync.Once
			releaseProvider := func() { providerOnce.Do(func() { close(providerGate) }) }
			releaseProjection := func() { projectionOnce.Do(func() { close(projectionGate) }) }
			service, _ := schedulerFixtureService(t, f, config, func(_ assessment.Request, _ *assessment.Response) {
				firstOnce.Do(func() { close(firstProvider) })
				<-providerGate
			})
			projections, _ := schedulerProjections(f, config, func(ctx context.Context, _ community.TopicProjectionBatch) error {
				select {
				case projectionEntered <- struct{}{}:
				default:
				}
				<-projectionGate
				return ctx.Err()
			}, nil)
			projections.before = func(ctx context.Context) {
				select {
				case <-firstProvider:
				case <-ctx.Done():
				}
			}
			service.SetProjectionRunner(projections)
			canonical := schedulerCanonical(t, f)
			ctx, stop := startScheduler(t, service, releaseProvider, 1)
			defer func() { releaseProjection(); stop() }()
			select {
			case <-projectionEntered:
			case <-time.After(30 * time.Second):
				t.Fatal("both work types were not admitted before the control")
			}
			if action == "pause" {
				_, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{OperationKey: "mixed-pause", Action: "pause"}, time.Now(), nil)
				require.NoError(t, err)
			} else if action == "disable" {
				_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
				require.NoError(t, err)
			}
			var before int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_attempts`).Row().Scan(&before))
			joined := make(chan struct{})
			if action == "cancel" {
				go func() { stop(); close(joined) }()
				<-ctx.Done()
			} else {
				releaseProvider()
			}
			require.Eventually(t, func() bool {
				var active int
				err := f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'`).Row().Scan(&active)
				return err == nil && active == 0
			}, 5*time.Second, 10*time.Millisecond)
			var projectionLeases int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM community_topic_work WHERE lease_until>clock_timestamp()`).Row().Scan(&projectionLeases))
			require.Positive(t, projectionLeases, "the publication latch must still own a real projection")
			if action == "cancel" {
				select {
				case <-joined:
					t.Fatal("scheduler returned before the admitted projection finalized")
				default:
				}
			}
			releaseProjection()
			if action == "cancel" {
				select {
				case <-joined:
				case <-time.After(5 * time.Second):
					t.Fatal("scheduler did not join the interrupted projection")
				}
			} else {
				require.Eventually(t, func() bool {
					var active int
					err := f.admin.Raw(`SELECT count(*) FROM community_topic_work WHERE lease_until>clock_timestamp()`).Row().Scan(&active)
					return err == nil && active == 0
				}, 5*time.Second, 10*time.Millisecond)
				stop()
			}
			var after int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_attempts`).Row().Scan(&after))
			if action != "cancel" {
				require.Equal(t, before, after, "stopping admission must not admit another provider attempt")
			}
			schedulerAssertDrained(t, f)
			require.Equal(t, canonical, schedulerCanonical(t, f))
		})
	}
}
