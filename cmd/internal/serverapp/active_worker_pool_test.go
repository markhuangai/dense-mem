package serverapp

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestActiveWorkerCountUsesConfiguredConcurrency(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "missing", in: 0, want: 1},
		{name: "negative", in: -2, want: 1},
		{name: "configured", in: 5, want: 5},
		{name: "high throughput", in: 30, want: 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activeWorkerCount(tt.in); got != tt.want {
				t.Fatalf("worker count = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestActiveTeamDispatcherProbesIdleTeamOncePerPoll(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	dispatcher := newActiveTeamDispatcher(5 * time.Second)
	dispatcher.replaceTeams([]string{"team-a"}, now)

	teamID, ok := dispatcher.next(now)
	if !ok || teamID != "team-a" {
		t.Fatalf("first dispatch = %q, %t; want team-a, true", teamID, ok)
	}
	if _, ok := dispatcher.next(now); ok {
		t.Fatal("idle team received a second concurrent probe")
	}

	dispatcher.complete("team-a", false, now)
	if _, ok := dispatcher.next(now.Add(4999 * time.Millisecond)); ok {
		t.Fatal("idle team was reprobed before its poll interval")
	}
	teamID, ok = dispatcher.next(now.Add(5 * time.Second))
	if !ok || teamID != "team-a" {
		t.Fatalf("poll dispatch = %q, %t; want team-a, true", teamID, ok)
	}
}

func TestActiveTeamDispatcherSaturatesHotTeamAndPrioritizesNewlyReadyTeam(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	dispatcher := newActiveTeamDispatcher(time.Second)
	dispatcher.replaceTeams([]string{"hot", "other"}, now)

	hot, ok := dispatcher.next(now)
	if !ok || hot != "hot" {
		t.Fatalf("first probe = %q, %t; want hot, true", hot, ok)
	}
	other, ok := dispatcher.next(now)
	if !ok || other != "other" {
		t.Fatalf("second probe = %q, %t; want other, true", other, ok)
	}
	dispatcher.complete("hot", true, now)
	for i := 0; i < 30; i++ {
		teamID, available := dispatcher.next(now)
		if !available || teamID != "hot" {
			t.Fatalf("hot dispatch %d = %q, %t; want hot, true", i, teamID, available)
		}
	}

	dispatcher.complete("other", true, now)
	teamID, available := dispatcher.next(now)
	if !available || teamID != "other" {
		t.Fatalf("dispatch after other became ready = %q, %t; want other, true", teamID, available)
	}
}

func TestActiveTeamWorkerPoolListsTeamsOnceBeforeIdlePoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	profiles := &activeWorkerProfileListStub{
		team: &domain.Profile{ID: uuid.MustParse("47e94a49-f8b5-493e-ae0a-4e1ff52e5b28")},
	}
	workCalls := make(chan struct{}, 30)
	go runActiveTeamWorkerPool(ctx, activeTeamWorkerPoolConfig{
		name:         "test",
		baseWorkerID: "worker",
		count:        30,
		pollInterval: 200 * time.Millisecond,
		profiles:     profiles,
		logger:       observability.New(slog.LevelError),
		workerError:  errors.New("test worker failed"),
		work: func(context.Context, string, string) (bool, error) {
			workCalls <- struct{}{}
			return false, nil
		},
	})

	select {
	case <-workCalls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the initial idle-team probe")
	}
	time.Sleep(50 * time.Millisecond)
	if got := profiles.calls.Load(); got != 1 {
		t.Fatalf("profile list calls before poll = %d, want 1", got)
	}
	if got := len(workCalls); got != 0 {
		t.Fatalf("extra work calls before poll = %d, want 0", got)
	}
}

type activeWorkerProfileListStub struct {
	team  *domain.Profile
	calls atomic.Int64
}

func (s *activeWorkerProfileListStub) List(_ context.Context, _, offset int) ([]*domain.Profile, error) {
	s.calls.Add(1)
	if offset > 0 {
		return nil, nil
	}
	return []*domain.Profile{s.team}, nil
}
