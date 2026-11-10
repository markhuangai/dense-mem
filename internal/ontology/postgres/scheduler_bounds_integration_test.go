//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type schedulerTurnObserver struct {
	ontology.MaintenanceRepository
	calls atomic.Int32
}

func (s *schedulerTurnObserver) ClaimMaintenanceTurn(ctx context.Context, window string, now time.Time, lease time.Duration) (*ontology.MaintenanceTurn, error) {
	s.calls.Add(1)
	return s.MaintenanceRepository.ClaimMaintenanceTurn(ctx, window, now, lease)
}

func TestOntologyMaintenanceParallelSchedulerAttemptBounds(t *testing.T) {
	f := newOrganizationFixture(t)
	for i := range 50 {
		f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("isolatedsource%03d", i), nil)
	}
	changes := make([]ontology.Change, 25)
	for i := range changes {
		changes[i] = ontology.Change{Record: testTopic(fmt.Sprintf("bounded-projection-%02d", i))}
	}
	_, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication(uuid.NewString(), 0, changes...))
	require.NoError(t, err)
	config := schedulerConfig(t, f, 1, 1)
	observer := &schedulerTurnObserver{MaintenanceRepository: f.store}
	service, _ := schedulerFixtureService(t, f, config, nil, observer)
	_, stop := startScheduler(t, service, nil, 1)
	defer stop()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.GreaterOrEqual(c, observer.calls.Load(), int32(20), "organization attempts")
	}, 45*time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return observer.calls.Load() > 20 }, time.Second, 10*time.Millisecond)
	stop()
	require.EqualValues(t, 20, observer.calls.Load())
	status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
	require.NoError(t, err)
	require.Positive(t, status.Counts.Pending, "the organization bound must leave real work for the next tick")
	require.False(t, status.CoverageComplete)
	schedulerAssertDrained(t, f)
}
