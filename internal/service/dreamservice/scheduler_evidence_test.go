package dreamservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSchedulerRunsHourlyEvidenceAndDailyGraphLanesIndependently(t *testing.T) {
	teamID := uuid.New()
	base := &schedulerDreamStub{cfg: dueSchedulerConfig()}
	dreams := &schedulerEvidenceStub{schedulerDreamStub: base}
	scheduler := NewScheduler(dreams, &schedulerProfileStub{profiles: []*domain.Team{{ID: teamID}}}, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []string{teamID.String()}, base.scheduledTeams)
	require.Equal(t, []time.Time{time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)}, dreams.evidenceWindows)
	require.Equal(t, []string{teamID.String()}, dreams.evidenceRecoveryTeams)
	require.True(t, scheduler.alreadyObserved(teamID.String(), "2026-09-04"))
	require.True(t, scheduler.hourlyAlreadyObserved(teamID.String(), "hour:2026-09-04T03"))

	scheduler.runDue(context.Background())
	require.Len(t, base.scheduledTeams, 1)
	require.Len(t, dreams.evidenceWindows, 1)
}

func TestSchedulerDoesNotCatchUpHourlyEvidenceAfterMissedHour(t *testing.T) {
	teamID := uuid.New()
	dreams := &schedulerEvidenceStub{schedulerDreamStub: &schedulerDreamStub{cfg: dueSchedulerConfig()}}
	scheduler := NewScheduler(dreams, &schedulerProfileStub{profiles: []*domain.Team{{ID: teamID}}}, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 9, 4, 3, 1, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Empty(t, dreams.evidenceWindows)
	require.False(t, scheduler.hourlyAlreadyObserved(teamID.String(), "hour:2026-09-04T03"))
}

func TestSchedulerDispatchesDailyGraphBeforeBlockingEvidenceLane(t *testing.T) {
	firstTeamID, secondTeamID := uuid.New(), uuid.New()
	started := make(chan struct{})
	release := make(chan struct{})
	dreams := &schedulerEvidenceStub{
		schedulerDreamStub: &schedulerDreamStub{cfg: dueSchedulerConfig()},
		evidenceStarted:    started,
		evidenceRelease:    release,
	}
	scheduler := NewScheduler(dreams, &schedulerProfileStub{profiles: []*domain.Team{{ID: firstTeamID}, {ID: secondTeamID}}}, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC) }
	done := make(chan struct{})
	go func() {
		scheduler.runDue(context.Background())
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("evidence lane did not start")
	}
	require.ElementsMatch(t, []string{firstTeamID.String(), secondTeamID.String()}, dreams.scheduledTeams,
		"all daily graph dispatches must complete before evidence can block")
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not finish after evidence lane release")
	}
}

func TestSchedulerSkipsEvidenceLaneForArchivedTeamButKeepsGraphDecision(t *testing.T) {
	teamID := uuid.New()
	base := &schedulerDreamStub{cfg: dueSchedulerConfig()}
	dreams := &schedulerEvidenceStub{schedulerDreamStub: base}
	scheduler := NewScheduler(dreams, &schedulerProfileStub{profiles: []*domain.Team{{ID: teamID, Status: "archived"}}}, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []string{teamID.String()}, base.scheduledTeams)
	require.Empty(t, dreams.evidenceWindows)
	require.Empty(t, dreams.evidenceRecoveryTeams)
}

func TestSchedulerPreservesEarlierDailyDispatchWhenLaterTeamPageFails(t *testing.T) {
	teams := make([]*domain.Team, schedulerTeamPageSize+1)
	for index := range teams {
		teams[index] = &domain.Team{ID: uuid.New()}
	}
	base := &schedulerDreamStub{cfg: dueSchedulerConfig()}
	dreams := &schedulerEvidenceStub{schedulerDreamStub: base}
	profiles := &schedulerProfileStub{profiles: teams, offsetErrs: map[int]error{schedulerTeamPageSize: errors.New("second page failed")}}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Len(t, base.scheduledTeams, schedulerTeamPageSize)
	require.Empty(t, dreams.evidenceWindows, "hourly evidence must wait until team enumeration succeeds")
}

type schedulerEvidenceStub struct {
	*schedulerDreamStub
	evidenceWindows       []time.Time
	evidenceRecoveryTeams []string
	evidenceStarted       chan struct{}
	evidenceRelease       chan struct{}
}

func (s *schedulerEvidenceStub) RunScheduledEvidenceCycle(_ context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error) {
	s.evidenceWindows = append(s.evidenceWindows, windowAt)
	if s.evidenceStarted != nil {
		close(s.evidenceStarted)
		s.evidenceStarted = nil
	}
	if s.evidenceRelease != nil {
		<-s.evidenceRelease
	}
	return &RunCycleResult{RunID: uuid.NewString(), TeamID: teamID, Status: "completed"}, nil
}

func (s *schedulerEvidenceStub) RecoverScheduledEvidenceCycle(_ context.Context, teamID string) (*RunCycleResult, error) {
	s.evidenceRecoveryTeams = append(s.evidenceRecoveryTeams, teamID)
	return nil, nil
}
