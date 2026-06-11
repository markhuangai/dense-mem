package dreamservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestIsDueAtUsesConfiguredLocalTime(t *testing.T) {
	cfg := EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
			StartTimeLocal: "03:00",
			Timezone:       "America/New_York",
			MaxOutputs:     5,
		},
	}

	require.True(t, isDueAt(time.Date(2026, 6, 11, 7, 0, 0, 0, time.UTC), cfg))
	require.False(t, isDueAt(time.Date(2026, 6, 11, 7, 1, 0, 0, time.UTC), cfg))
}

func TestIsDueAtRejectsInvalidStartTime(t *testing.T) {
	cfg := EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
			StartTimeLocal: "bad",
			Timezone:       "UTC",
			MaxOutputs:     5,
		},
	}

	require.False(t, isDueAt(time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC), cfg))
}

func TestSchedulerPagesThroughProfiles(t *testing.T) {
	profiles := make([]*domain.Profile, 101)
	for i := range profiles {
		profiles[i] = &domain.Profile{ID: uuid.New()}
	}
	profileSvc := &schedulerProfileStub{profiles: profiles}
	dreamSvc := &schedulerDreamStub{cfg: EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
			Enabled:           true,
			StartTimeLocal:    "03:00",
			Timezone:          "UTC",
			ReflectEnabled:    true,
			ReevaluateEnabled: true,
			DreamEnabled:      true,
			MaxOutputs:        5,
		},
	}}
	scheduler := NewScheduler(dreamSvc, profileSvc, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []int{0, schedulerProfilePageSize}, profileSvc.offsets)
	require.Equal(t, 101, dreamSvc.runs)
}

func TestSchedulerStartReturnsWhenUnavailableOrCanceled(t *testing.T) {
	NewScheduler(nil, nil, nil).Start(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewScheduler(&schedulerDreamStub{}, &schedulerProfileStub{}, discardSchedulerLogger()).Start(ctx)
}

func TestSchedulerRunDueSkipsAndLogsNonRunnableProfiles(t *testing.T) {
	configErr := uuid.New()
	disabled := uuid.New()
	noPhases := uuid.New()
	notDue := uuid.New()
	alreadyRan := uuid.New()
	runErr := uuid.New()
	runOK := uuid.New()
	profileSvc := &schedulerProfileStub{profiles: []*domain.Profile{
		nil,
		{ID: configErr},
		{ID: disabled},
		{ID: noPhases},
		{ID: notDue},
		{ID: alreadyRan},
		{ID: runErr},
		{ID: runOK},
	}}
	due := EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
		Enabled:           true,
		StartTimeLocal:    "03:00",
		Timezone:          "UTC",
		ReflectEnabled:    true,
		ReevaluateEnabled: true,
		DreamEnabled:      true,
		MaxOutputs:        5,
	}}
	dreamSvc := &schedulerDreamStub{
		cfg: due,
		cfgErrs: map[string]error{
			configErr.String(): errors.New("config failed"),
		},
		cfgByProfile: map[string]EffectiveConfig{
			disabled.String(): {DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
				Enabled:        false,
				StartTimeLocal: "03:00",
				Timezone:       "UTC",
				MaxOutputs:     5,
			}},
			noPhases.String(): {DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
				Enabled:        true,
				StartTimeLocal: "03:00",
				Timezone:       "UTC",
				MaxOutputs:     5,
			}},
			notDue.String(): {DreamingRuntimeConfig: domain.DreamingRuntimeConfig{
				Enabled:        true,
				StartTimeLocal: "04:00",
				Timezone:       "UTC",
				ReflectEnabled: true,
				MaxOutputs:     5,
			}},
		},
		runErrs: map[string]error{
			runErr.String(): errors.New("run failed"),
		},
	}
	scheduler := NewScheduler(dreamSvc, profileSvc, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC) }
	scheduler.markRan(alreadyRan.String(), "2026-06-11")

	scheduler.runDue(context.Background())

	require.Equal(t, 2, dreamSvc.runs)
	require.Equal(t, []string{runErr.String(), runOK.String()}, dreamSvc.runProfiles)
	require.True(t, scheduler.alreadyRan(runOK.String(), "2026-06-11"))
}

func TestSchedulerRunDueStopsOnProfileListError(t *testing.T) {
	profileSvc := &schedulerProfileStub{err: errors.New("list failed")}
	dreamSvc := &schedulerDreamStub{}
	scheduler := NewScheduler(dreamSvc, profileSvc, discardSchedulerLogger())

	scheduler.runDue(context.Background())

	require.Equal(t, []int{0}, profileSvc.offsets)
	require.Zero(t, dreamSvc.runs)
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
	cfg          EffectiveConfig
	cfgByProfile map[string]EffectiveConfig
	cfgErrs      map[string]error
	runErrs      map[string]error
	runProfiles  []string
	runs         int
}

func (s *schedulerDreamStub) RunCycle(_ context.Context, profileID string, _ RunCycleRequest) (*RunCycleResult, error) {
	s.runs++
	s.runProfiles = append(s.runProfiles, profileID)
	if err := s.runErrs[profileID]; err != nil {
		return nil, err
	}
	return &RunCycleResult{RunID: uuid.NewString()}, nil
}

func (s *schedulerDreamStub) List(context.Context, string, ListOptions) ([]*domain.Dream, string, error) {
	return nil, "", nil
}

func (s *schedulerDreamStub) Get(context.Context, string, string) (*domain.Dream, error) {
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

func (s *schedulerDreamStub) EffectiveConfig(_ context.Context, profileID string) (EffectiveConfig, error) {
	if err := s.cfgErrs[profileID]; err != nil {
		return EffectiveConfig{}, err
	}
	if cfg, ok := s.cfgByProfile[profileID]; ok {
		return cfg, nil
	}
	return s.cfg, nil
}
