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
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
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

func TestActiveWorkerFailureLogErrorUsesBoundedDiagnosticContext(t *testing.T) {
	base := errors.New("semantic placement worker failed")
	failure := memoryservice.PlacementWorkerFailure{
		SubmissionID: "submission-1",
		Stage:        "assessment",
		ReasonCode:   "assessor_provider_failed",
		Class:        "timeout",
	}

	if got := activeWorkerFailureLogError(base, failure, true).Error(); got != "semantic placement worker failed; submission_id=submission-1; stage=assessment; reason=assessor_provider_failed; class=timeout" {
		t.Fatalf("diagnostic error = %q", got)
	}
	if got := activeWorkerFailureLogError(base, failure, false); got != base {
		t.Fatalf("unclassified error = %v, want original error", got)
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

func TestActiveTeamDispatcherDrainsInflightWorkBeforeCooling(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name               string
		remaining          []bool
		resumesImmediately bool
	}{
		{name: "work then idle", remaining: []bool{true, false}, resumesImmediately: true},
		{name: "idle then work", remaining: []bool{false, true}, resumesImmediately: true},
		{name: "all idle", remaining: []bool{false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := newActiveTeamDispatcher(time.Second)
			dispatcher.replaceTeams([]string{"team-a"}, now)

			teamID, ok := dispatcher.next(now)
			if !ok || teamID != "team-a" {
				t.Fatalf("initial probe = %q, %t; want team-a, true", teamID, ok)
			}
			dispatcher.complete("team-a", true, now)

			for i := 0; i <= len(tt.remaining); i++ {
				teamID, ok = dispatcher.next(now)
				if !ok || teamID != "team-a" {
					t.Fatalf("concurrent dispatch %d = %q, %t; want team-a, true", i, teamID, ok)
				}
			}
			dispatcher.complete("team-a", false, now)
			if _, ok := dispatcher.next(now); ok {
				t.Fatal("team dispatched while in-flight work was draining")
			}
			for i, worked := range tt.remaining {
				dispatcher.complete("team-a", worked, now)
				if i < len(tt.remaining)-1 {
					if _, ok := dispatcher.next(now); ok {
						t.Fatal("team dispatched before all in-flight work drained")
					}
				}
			}

			teamID, ok = dispatcher.next(now)
			if tt.resumesImmediately {
				if !ok || teamID != "team-a" {
					t.Fatalf("dispatch after drain = %q, %t; want team-a, true", teamID, ok)
				}
				return
			}
			if ok {
				t.Fatalf("idle team dispatched immediately after drain: %q", teamID)
			}
			teamID, ok = dispatcher.next(now.Add(time.Second))
			if !ok || teamID != "team-a" {
				t.Fatalf("poll dispatch = %q, %t; want team-a, true", teamID, ok)
			}
		})
	}
}

func TestActiveTeamDispatcherPreservesWorkSeenBeforeDrain(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	dispatcher := newActiveTeamDispatcher(time.Second)
	dispatcher.replaceTeams([]string{"team-a"}, now)

	teamID, ok := dispatcher.next(now)
	if !ok || teamID != "team-a" {
		t.Fatalf("initial probe = %q, %t; want team-a, true", teamID, ok)
	}
	dispatcher.complete("team-a", true, now)

	for i := 0; i < 2; i++ {
		teamID, ok = dispatcher.next(now)
		if !ok || teamID != "team-a" {
			t.Fatalf("concurrent dispatch %d = %q, %t; want team-a, true", i, teamID, ok)
		}
	}
	dispatcher.complete("team-a", true, now)
	dispatcher.complete("team-a", false, now)

	teamID, ok = dispatcher.next(now)
	if !ok || teamID != "team-a" {
		t.Fatalf("dispatch after mixed burst = %q, %t; want team-a, true", teamID, ok)
	}
}

func TestActiveTeamWorkerPoolListsTeamsOnceBeforeIdlePoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	profiles := &activeWorkerProfileListStub{
		team: &domain.Team{ID: uuid.MustParse("47e94a49-f8b5-493e-ae0a-4e1ff52e5b28")},
	}
	workCalls := make(chan struct{}, 30)
	go runActiveTeamWorkerPool(ctx, activeTeamWorkerPoolConfig{
		name:         "test",
		baseWorkerID: "worker",
		count:        30,
		pollInterval: 200 * time.Millisecond,
		teams:        profiles,
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

func TestActiveTeamWorkerPoolKeepsClaimedTeamHotAfterWorkerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	profiles := &activeWorkerProfileListStub{
		team: &domain.Team{ID: uuid.MustParse("47e94a49-f8b5-493e-ae0a-4e1ff52e5b28")},
	}
	workCalls := make(chan int, 2)
	var calls atomic.Int32
	go runActiveTeamWorkerPool(ctx, activeTeamWorkerPoolConfig{
		name:         "test",
		baseWorkerID: "worker",
		count:        1,
		pollInterval: 5 * time.Second,
		teams:        profiles,
		logger:       observability.New(slog.LevelError),
		workerError:  errors.New("test worker failed"),
		work: func(context.Context, string, string) (bool, error) {
			call := int(calls.Add(1))
			workCalls <- call
			if call == 1 {
				return true, errors.New("claimed work failed")
			}
			return false, nil
		},
	})

	for want := 1; want <= 2; want++ {
		select {
		case got := <-workCalls:
			if got != want {
				t.Fatalf("work call = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for work call %d before the poll interval", want)
		}
	}
}

type activeWorkerProfileListStub struct {
	team  *domain.Team
	calls atomic.Int64
}

func (s *activeWorkerProfileListStub) List(_ context.Context, _, offset int) ([]*domain.Team, error) {
	s.calls.Add(1)
	if offset > 0 {
		return nil, nil
	}
	return []*domain.Team{s.team}, nil
}
