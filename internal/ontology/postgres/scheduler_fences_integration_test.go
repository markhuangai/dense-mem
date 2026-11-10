//go:build integration

package postgres

import (
	"context"

	"testing"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

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
	observer := &schedulerTurnObserver{MaintenanceRepository: f.store}
	service, calls := schedulerFixtureService(t, f, config, nil, observer)
	_, stop := startScheduler(t, service, nil, 1)
	defer stop()
	require.Eventually(t, func() bool { return observer.calls.Load() > 0 }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	stop()
	require.Zero(t, calls())
	require.LessOrEqual(t, observer.calls.Load(), int32(2), "unavailable capacity must not spin")
}
