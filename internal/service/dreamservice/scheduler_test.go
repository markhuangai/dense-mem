package dreamservice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestScheduledWindowAtUsesConfiguredLocalTime(t *testing.T) {
	cfg := EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:00",
		Timezone:       "America/New_York",
		MaxOutputs:     5,
	}}

	require.Equal(t, scheduledWindowDue, scheduledWindowAt(time.Date(2026, 6, 11, 7, 0, 0, 0, time.UTC), cfg))
	require.Equal(t, scheduledWindowMissed, scheduledWindowAt(time.Date(2026, 6, 11, 7, 1, 0, 0, time.UTC), cfg))
	require.Equal(t, scheduledWindowNotDue, scheduledWindowAt(time.Date(2026, 6, 11, 6, 59, 0, 0, time.UTC), cfg))
}

func TestScheduledWindowAtRecordsNonexistentDSTMinuteAsMissed(t *testing.T) {
	cfg := EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "02:30",
		Timezone:       "America/New_York",
		MaxOutputs:     5,
	}}

	require.Equal(t, scheduledWindowNotDue, scheduledWindowAt(time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC), cfg))
	require.Equal(t, scheduledWindowMissed, scheduledWindowAt(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), cfg))
}

func TestScheduledWindowHelpersRejectInvalidStartTime(t *testing.T) {
	cfg := EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "invalid",
		Timezone:       "UTC",
		MaxOutputs:     5,
	}}
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)

	require.Equal(t, scheduledWindowNotDue, scheduledWindowAt(now, cfg))
	_, ok := scheduledWindowAtTime(now, cfg)
	require.False(t, ok)
}

func TestScheduledWindowHelpersRejectInvalidTimezone(t *testing.T) {
	cfg := EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:00",
		Timezone:       "not-a-timezone",
		MaxOutputs:     5,
	}}
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)

	require.Equal(t, scheduledWindowNotDue, scheduledWindowAt(now, cfg))
	_, ok := scheduledWindowAtTime(now, cfg)
	require.False(t, ok)
}

func TestSchedulerStartReturnsWhenUnavailableOrCanceled(t *testing.T) {
	NewScheduler(nil, nil, nil).Start(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewScheduler(&schedulerDreamStub{cfg: dueSchedulerConfig()}, &schedulerProfileStub{}, discardSchedulerLogger()).Start(ctx)
}

func TestSchedulerPagesThroughTeamsAndUsesScheduledPath(t *testing.T) {
	teams := make([]*domain.Profile, 101)
	for i := range teams {
		teams[i] = &domain.Profile{ID: uuid.New()}
	}
	profiles := &schedulerProfileStub{profiles: teams}
	dreams := &schedulerDreamStub{cfg: dueSchedulerConfig()}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []int{0, schedulerProfilePageSize}, profiles.offsets)
	require.Len(t, dreams.scheduledTeams, 101)
	require.Empty(t, dreams.missedTeams)
}

func TestSchedulerDoesNotRecordMissedWindowWhenFirstObservedLate(t *testing.T) {
	teamID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}
	dreams := &schedulerDreamStub{cfg: dueSchedulerConfig()}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 1, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())
	scheduler.runDue(context.Background())

	require.Empty(t, dreams.scheduledTeams)
	require.Empty(t, dreams.missedTeams)
	require.False(t, scheduler.alreadyObserved(teamID.String(), "2026-06-11"))
}

func TestSchedulerDoesNotRecordPreviousLocalWindowWhenFirstObservedOvernight(t *testing.T) {
	teamID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}
	dreams := &schedulerDreamStub{cfg: EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "23:00",
		Timezone:       "America/New_York",
		MaxOutputs:     5,
	}}}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 12, 5, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Empty(t, dreams.scheduledTeams)
	require.Empty(t, dreams.missedTeams)
	require.Empty(t, dreams.missedRunDates)
	require.False(t, scheduler.alreadyObserved(teamID.String(), "2026-06-11"))
}

func TestSchedulerRecordsMissedWindowAfterArmingBeforeWindow(t *testing.T) {
	teamID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}
	dreams := &schedulerDreamStub{cfg: dueSchedulerConfig()}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 2, 59, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.True(t, scheduler.isArmed(teamID.String(), scheduledWindowArm{
		runDate:        "2026-06-11",
		startTimeLocal: "03:00",
		timezone:       "UTC",
	}))
	require.Empty(t, dreams.scheduledTeams)
	require.Empty(t, dreams.missedTeams)

	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 1, 0, 0, time.UTC) }
	scheduler.runDue(context.Background())

	require.Empty(t, dreams.scheduledTeams)
	require.Equal(t, []string{teamID.String()}, dreams.missedTeams)
	require.True(t, scheduler.alreadyObserved(teamID.String(), "2026-06-11"))
}

func TestSchedulerDoesNotObserveSkippedMissedWindow(t *testing.T) {
	teamID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}
	dreams := &schedulerDreamStub{cfg: dueSchedulerConfig(), missedStatus: "skipped"}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 2, 59, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 1, 0, 0, time.UTC) }
	scheduler.runDue(context.Background())

	require.Equal(t, []string{teamID.String()}, dreams.missedTeams)
	require.False(t, scheduler.alreadyObserved(teamID.String(), "2026-06-11"))
	require.True(t, scheduler.isArmed(teamID.String(), scheduledWindowArm{
		runDate:        "2026-06-11",
		startTimeLocal: "03:00",
		timezone:       "UTC",
	}))
}

func TestSchedulerRecordsMissedWindowAfterDueCycleSkips(t *testing.T) {
	teamID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}
	dreams := &schedulerDreamStub{cfg: dueSchedulerConfig(), scheduledStatus: "skipped"}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []string{teamID.String()}, dreams.scheduledTeams)
	require.False(t, scheduler.alreadyObserved(teamID.String(), "2026-06-11"))

	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 1, 0, 0, time.UTC) }
	scheduler.runDue(context.Background())

	require.Equal(t, []string{teamID.String()}, dreams.missedTeams)
	require.True(t, scheduler.alreadyObserved(teamID.String(), "2026-06-11"))
}

func TestSchedulerRecordsNonexistentDSTWindowWithoutRunningEarly(t *testing.T) {
	teamID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}
	dreams := &schedulerDreamStub{cfg: EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "02:30",
		Timezone:       "America/New_York",
		MaxOutputs:     5,
	}}}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Empty(t, dreams.scheduledTeams)
	require.Empty(t, dreams.missedTeams)
	require.False(t, scheduler.alreadyObserved(teamID.String(), "2026-03-08"))
	require.True(t, scheduler.isArmed(teamID.String(), scheduledWindowArm{
		runDate:        "2026-03-08",
		startTimeLocal: "02:30",
		timezone:       "America/New_York",
	}))

	scheduler.now = func() time.Time { return time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC) }
	scheduler.runDue(context.Background())

	require.Empty(t, dreams.scheduledTeams)
	require.Equal(t, []string{teamID.String()}, dreams.missedTeams)
	require.True(t, scheduler.alreadyObserved(teamID.String(), "2026-03-08"))
}

func TestSchedulerRunsOnlyOnceAcrossFallBackDuplicateMinute(t *testing.T) {
	teamID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}
	dreams := &schedulerDreamStub{cfg: EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "01:30",
		Timezone:       "America/New_York",
		MaxOutputs:     5,
	}}}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []string{teamID.String()}, dreams.scheduledTeams)

	scheduler.now = func() time.Time { return time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC) }
	scheduler.runDue(context.Background())

	require.Equal(t, []string{teamID.String()}, dreams.scheduledTeams)
	require.Empty(t, dreams.missedTeams)
}

func TestSchedulerContinuesAfterPerTeamErrorAndStopsOnListError(t *testing.T) {
	failedID := uuid.New()
	successID := uuid.New()
	profiles := &schedulerProfileStub{profiles: []*domain.Profile{{ID: failedID}, {ID: successID}}}
	dreams := &schedulerDreamStub{
		cfg:           dueSchedulerConfig(),
		scheduledErrs: map[string]error{failedID.String(): errors.New("scheduled failure")},
	}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []string{failedID.String(), successID.String()}, dreams.scheduledTeams)
	require.False(t, scheduler.alreadyObserved(failedID.String(), "2026-06-11"))
	require.True(t, scheduler.alreadyObserved(successID.String(), "2026-06-11"))

	failingProfiles := &schedulerProfileStub{err: errors.New("list failed")}
	NewScheduler(dreams, failingProfiles, discardSchedulerLogger()).runDue(context.Background())
	require.Equal(t, []int{0}, failingProfiles.offsets)
}

func TestSchedulerLogsBoundedErrorsAndSkipsUnavailableSchedules(t *testing.T) {
	const rawError = "database password=secret"
	teamID := uuid.New()
	for _, tc := range []struct {
		name     string
		profiles *schedulerProfileStub
		dreams   *schedulerDreamStub
		wantKind string
	}{
		{
			name:     "team list",
			profiles: &schedulerProfileStub{err: errors.New(rawError)},
			dreams:   &schedulerDreamStub{cfg: dueSchedulerConfig()},
			wantKind: "team_list_failed",
		},
		{
			name:     "config resolve",
			profiles: &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}},
			dreams:   &schedulerDreamStub{cfgErrs: map[string]error{teamID.String(): errors.New(rawError)}},
			wantKind: "config_resolve_failed",
		},
		{
			name:     "cycle",
			profiles: &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}},
			dreams: &schedulerDreamStub{
				cfg:           dueSchedulerConfig(),
				scheduledErrs: map[string]error{teamID.String(): errors.New(rawError)},
			},
			wantKind: "cycle_failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			scheduler := NewScheduler(tc.dreams, tc.profiles, slog.New(slog.NewTextHandler(&logs, nil)))
			scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) }
			scheduler.runDue(context.Background())

			assert.Contains(t, logs.String(), "error_kind="+tc.wantKind)
			assert.NotContains(t, logs.String(), rawError)
		})
	}

	var logs bytes.Buffer
	dreams := &schedulerDreamStub{cfg: EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:00",
		Timezone:       "not-a-timezone",
		MaxOutputs:     5,
	}}}
	scheduler := NewScheduler(dreams, &schedulerProfileStub{profiles: []*domain.Profile{{ID: teamID}}}, slog.New(slog.NewTextHandler(&logs, nil)))
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) }
	scheduler.runDue(context.Background())

	require.Empty(t, dreams.scheduledTeams)
	assert.Contains(t, logs.String(), "error_kind=schedule_unavailable")
}

func TestSchedulerPrunesObservedEntries(t *testing.T) {
	scheduler := NewScheduler(&schedulerDreamStub{}, &schedulerProfileStub{}, discardSchedulerLogger())
	scheduler.observed = map[string]string{
		"stale":   "2026-06-03",
		"recent":  "2026-06-10",
		"invalid": "not-a-date",
	}
	scheduler.armed = map[string]scheduledWindowArm{
		"stale":   {runDate: "2026-06-03"},
		"recent":  {runDate: "2026-06-10"},
		"invalid": {runDate: "not-a-date"},
	}

	scheduler.pruneObserved(time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC))

	require.NotContains(t, scheduler.observed, "stale")
	require.NotContains(t, scheduler.observed, "invalid")
	require.Equal(t, "2026-06-10", scheduler.observed["recent"])
	require.NotContains(t, scheduler.armed, "stale")
	require.NotContains(t, scheduler.armed, "invalid")
	require.Equal(t, "2026-06-10", scheduler.armed["recent"].runDate)
}

func dueSchedulerConfig() EffectiveConfig {
	return EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:00",
		Timezone:       "UTC",
		MaxOutputs:     5,
	}}
}

type schedulerProfileStub struct {
	profiles []*domain.Profile
	offsets  []int
	err      error
}

func discardSchedulerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (s *schedulerProfileStub) GetByID(context.Context, uuid.UUID) (*domain.Profile, error) {
	return nil, nil
}

func (s *schedulerProfileStub) List(_ context.Context, limit, offset int) ([]*domain.Profile, error) {
	s.offsets = append(s.offsets, offset)
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

type schedulerDreamStub struct {
	cfg              EffectiveConfig
	cfgByTeam        map[string]EffectiveConfig
	cfgErrs          map[string]error
	scheduledErrs    map[string]error
	missedErrs       map[string]error
	scheduledStatus  string
	missedStatus     string
	scheduledTeams   []string
	missedTeams      []string
	scheduledWindows []time.Time
	missedRunDates   []string
}

func (s *schedulerDreamStub) RunCycle(context.Context, string, RunCycleRequest) (*RunCycleResult, error) {
	return nil, errors.New("unexpected actor-bound cycle")
}

func (s *schedulerDreamStub) RunScheduledCycle(_ context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error) {
	s.scheduledTeams = append(s.scheduledTeams, teamID)
	s.scheduledWindows = append(s.scheduledWindows, windowAt)
	if err := s.scheduledErrs[teamID]; err != nil {
		return nil, err
	}
	status := s.scheduledStatus
	if status == "" {
		status = "completed"
	}
	return &RunCycleResult{RunID: uuid.NewString(), TeamID: teamID, Status: status}, nil
}

func (s *schedulerDreamStub) RecordMissedScheduledCycle(_ context.Context, teamID, runDate string) (*RunCycleResult, error) {
	s.missedTeams = append(s.missedTeams, teamID)
	s.missedRunDates = append(s.missedRunDates, runDate)
	if err := s.missedErrs[teamID]; err != nil {
		return nil, err
	}
	status := s.missedStatus
	if status == "" {
		status = "missed"
	}
	return &RunCycleResult{RunID: uuid.NewString(), TeamID: teamID, Status: status}, nil
}

func (s *schedulerDreamStub) List(context.Context, string, ListOptions) ([]*domain.Dream, string, error) {
	return nil, "", nil
}

func (s *schedulerDreamStub) Get(context.Context, string, string) (*domain.Dream, error) {
	return nil, nil
}

func (s *schedulerDreamStub) ListRuns(context.Context, string, int) ([]*RunCycleResult, error) {
	return nil, nil
}

func (s *schedulerDreamStub) Recall(context.Context, string, string, int) ([]*domain.Dream, error) {
	return nil, nil
}

func (s *schedulerDreamStub) ResolveFeedback(context.Context, string, ResolveFeedbackRequest) (*ResolveFeedbackResult, error) {
	return &ResolveFeedbackResult{}, nil
}

func (s *schedulerDreamStub) Status(context.Context, string) (*StatusResult, error) {
	return nil, nil
}

func (s *schedulerDreamStub) EffectiveConfig(_ context.Context, teamID string) (EffectiveConfig, error) {
	if err := s.cfgErrs[teamID]; err != nil {
		return EffectiveConfig{}, err
	}
	if cfg, ok := s.cfgByTeam[teamID]; ok {
		return cfg, nil
	}
	return s.cfg, nil
}

var _ Service = (*schedulerDreamStub)(nil)
var _ ProfileService = (*schedulerProfileStub)(nil)
