//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type schedulerTurnObserver struct {
	topic.MaintenanceRepository
	calls atomic.Int32
}

func (s *schedulerTurnObserver) ClaimMaintenanceTurn(ctx context.Context, window string, now time.Time, lease time.Duration) (*topic.MaintenanceTurn, error) {
	s.calls.Add(1)
	return s.MaintenanceRepository.ClaimMaintenanceTurn(ctx, window, now, lease)
}

func TestOntologyMaintenanceParallelSchedulerAttemptBounds(t *testing.T) {
	f := newOrganizationFixture(t)
	for i := range 50 {
		f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("isolatedsource%03d", i), nil)
	}
	changes := make([]topic.Change, 401)
	for i := range changes {
		changes[i] = topic.Change{Record: testTopic(fmt.Sprintf("uniqueprojection%04d", i))}
	}
	var revision int64
	for offset := 0; offset < len(changes); offset += topic.MaxChanges {
		result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), revision, changes[offset:min(offset+topic.MaxChanges, len(changes))]...))
		require.NoError(t, err)
		revision = result.Revision
	}
	config := schedulerConfig(t, f, 1, 1)
	observer := &schedulerTurnObserver{MaintenanceRepository: f.store}
	service, _ := schedulerFixtureService(t, f, config, nil, observer)
	_, stop := startScheduler(t, service, nil, 1)
	defer stop()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.GreaterOrEqual(c, observer.calls.Load(), int32(20), "organization attempts")
	}, 55*time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return observer.calls.Load() > 20 }, time.Second, 10*time.Millisecond)
	stop()
	require.EqualValues(t, 20, observer.calls.Load())
	status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 50, status.Counts.Organized)
	require.Positive(t, status.Communities.Standalone, "the shared scheduler must complete real Community work")
	require.Positive(t, status.Communities.Pending, "the shared turn bound must leave real placement work for the next tick")
	require.False(t, status.CoverageComplete)
	schedulerAssertDrained(t, f)
}
