//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"

	"github.com/stretchr/testify/require"
)

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
			concurrencySources(t, f)
			config := schedulerConfig(t, f, 4, 4)
			gate, entered := make(chan struct{}), make(chan struct{})
			var releaseOnce, entryOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate) }) }
			service, _ := schedulerFixtureService(t, f, config, func(_ assessment.Request, _ *assessment.Response) { entryOnce.Do(func() { close(entered) }); <-gate })
			canonical := schedulerCanonical(t, f)
			ctx, stop := startScheduler(t, service, release, 1)
			defer stop()
			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("provider did not enter before control")
			}
			if action == "pause" {
				_, err := f.store.MaintenanceCommand(context.Background(), domain.OntologyMaintenanceCommand{OperationKey: "evidence-pause", Action: "pause"}, time.Now(), nil)
				require.NoError(t, err)
			} else if action == "disable" {
				_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "false"}, "control", "", "")
				require.NoError(t, err)
			}
			var before int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_attempts`).Row().Scan(&before))
			if action == "cancel" {
				joined := make(chan struct{})
				go func() { stop(); close(joined) }()
				<-ctx.Done()
				select {
				case <-joined:
				case <-time.After(10 * time.Second):
					t.Fatal("scheduler did not join cancelled work")
				}
			} else {
				release()
				require.Eventually(t, func() bool {
					var active int
					err := f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'`).Row().Scan(&active)
					return err == nil && active == 0
				}, 10*time.Second, 10*time.Millisecond)
				stop()
				var after int
				require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_attempts`).Row().Scan(&after))
				require.Equal(t, before, after, "stopped admission cannot create a new provider attempt")
			}
			schedulerAssertDrained(t, f)
			require.Equal(t, canonical, schedulerCanonical(t, f))
		})
	}
}
