package communityservice

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSchedulerWaitsForProfileRunsOnCancellation(t *testing.T) {
	service := &schedulerServiceStub{started: make(chan struct{})}
	profiles := &schedulerProfilesStub{profiles: []*domain.Profile{{ID: uuid.New()}}}
	scheduler := NewScheduler(service, profiles, schedulerConfigStub{}, nil)
	scheduler.now = func() time.Time { return time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		scheduler.runDue(ctx)
		close(done)
	}()
	<-service.started
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not wait for the launched run to exit")
	}
}

func TestSchedulerStartHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler := NewScheduler(&schedulerServiceStub{started: make(chan struct{})}, schedulerProfilesStub{}, schedulerConfigStub{}, nil)
	scheduler.Start(ctx)
}

func TestSchedulerStartRunsBoundaryThenStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := NewScheduler(&schedulerServiceStub{started: make(chan struct{})}, schedulerProfilesStub{}, schedulerConfigStub{
		cfg: domain.CommunityDetectionRuntimeConfig{Enabled: false, StartTimeLocal: "03:00", Timezone: "UTC"},
	}, nil)
	boundary := time.Now().UTC().Truncate(time.Minute).Add(time.Minute).Add(-time.Millisecond)
	scheduler.now = func() time.Time {
		return boundary
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	scheduler.Start(ctx)
}

type schedulerConfigStub struct {
	cfg domain.CommunityDetectionRuntimeConfig
	err error
}

func (s schedulerConfigStub) CommunityDetectionRuntimeConfig(context.Context) (domain.CommunityDetectionRuntimeConfig, error) {
	if s.err != nil {
		return domain.CommunityDetectionRuntimeConfig{}, s.err
	}
	if s.cfg == (domain.CommunityDetectionRuntimeConfig{}) {
		return domain.CommunityDetectionRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxConcurrency: 1}, nil
	}
	return s.cfg, nil
}

type schedulerProfilesStub struct {
	profiles []*domain.Profile
	err      error
}

func (s schedulerProfilesStub) List(_ context.Context, limit, offset int) ([]*domain.Profile, error) {
	if s.err != nil {
		return nil, s.err
	}
	if offset >= len(s.profiles) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.profiles) {
		end = len(s.profiles)
	}
	return s.profiles[offset:end], nil
}

type schedulerServiceStub struct {
	started chan struct{}
}

func (s *schedulerServiceStub) RunScheduled(ctx context.Context, _ string, _ time.Time) (*RunResult, error) {
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*schedulerServiceStub) Status(context.Context, string) (*StatusResult, error) {
	return nil, nil
}

var _ AppConfig = schedulerConfigStub{}
var _ ProfileService = schedulerProfilesStub{}
var _ Service = (*schedulerServiceStub)(nil)
