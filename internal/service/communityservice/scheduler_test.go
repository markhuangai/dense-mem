package communityservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func TestScheduledCommunityRunUsesConfiguredLocalTime(t *testing.T) {
	cfg := domain.CommunityDetectionRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:30",
		Timezone:       "America/New_York",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	}

	runDate, dueAt, due := scheduledCommunityRun(time.Date(2026, 6, 15, 7, 30, 0, 0, time.UTC), "profile-1", cfg)
	require.True(t, due)
	require.Equal(t, "2026-06-15", runDate)
	require.Equal(t, 3, dueAt.Hour())
	require.Equal(t, 30, dueAt.Minute())

	_, _, due = scheduledCommunityRun(time.Date(2026, 6, 15, 7, 29, 59, 0, time.UTC), "profile-1", cfg)
	require.False(t, due)
}

func TestCommunitySchedulerSkipsDisabledAndNotDueConfig(t *testing.T) {
	profileID := uuid.New()
	profiles := &communitySchedulerProfileStub{profiles: []*domain.Profile{{ID: profileID}}}
	detector := &communitySchedulerDetectorStub{}
	config := &communitySchedulerConfigStub{cfg: domain.CommunityDetectionRuntimeConfig{
		Enabled:        false,
		StartTimeLocal: "03:30",
		Timezone:       "UTC",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	}}
	scheduler := NewScheduler(detector, profiles, config, nil, discardCommunitySchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())
	require.Empty(t, detector.profiles)
	require.Empty(t, profiles.offsets)

	config.cfg.Enabled = true
	scheduler.now = func() time.Time { return time.Date(2026, 6, 15, 3, 29, 0, 0, time.UTC) }
	scheduler.runDue(context.Background())
	require.Empty(t, detector.profiles)
	require.Equal(t, []int{0}, profiles.offsets)
}

func TestCommunitySchedulerStartReturnsWhenDetectorUnavailableOrCanceled(t *testing.T) {
	NewScheduler(nil, nil, nil, nil, nil).Start(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewScheduler(&communitySchedulerDetectorStub{}, &communitySchedulerProfileStub{}, &communitySchedulerConfigStub{}, nil, discardCommunitySchedulerLogger()).Start(ctx)
}

func TestCommunitySchedulerPagesThroughProfiles(t *testing.T) {
	profiles := make([]*domain.Profile, 101)
	for i := range profiles {
		profiles[i] = &domain.Profile{ID: uuid.New()}
	}
	profileSvc := &communitySchedulerProfileStub{profiles: profiles}
	detector := &communitySchedulerDetectorStub{}
	config := &communitySchedulerConfigStub{cfg: domain.CommunityDetectionRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:30",
		Timezone:       "UTC",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	}}
	scheduler := NewScheduler(detector, profileSvc, config, nil, discardCommunitySchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Equal(t, []int{0, schedulerProfilePageSize}, profileSvc.offsets)
	require.Len(t, detector.profiles, 101)
}

func TestCommunitySchedulerMarksAttemptsAndContinuesAfterErrors(t *testing.T) {
	tooLarge := uuid.New()
	runErr := uuid.New()
	runOK := uuid.New()
	profileSvc := &communitySchedulerProfileStub{profiles: []*domain.Profile{
		nil,
		{ID: tooLarge},
		{ID: runErr},
		{ID: runOK},
	}}
	detector := &communitySchedulerDetectorStub{
		errs: map[string]error{
			tooLarge.String(): ErrCommunityGraphTooLarge,
			runErr.String():   errors.New("detect failed"),
		},
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	config := &communitySchedulerConfigStub{cfg: domain.CommunityDetectionRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:30",
		Timezone:       "UTC",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	}}
	scheduler := NewScheduler(detector, profileSvc, config, metrics, discardCommunitySchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())
	scheduler.runDue(context.Background())

	require.Equal(t, []string{tooLarge.String(), runErr.String(), runOK.String()}, detector.profiles)
	require.True(t, scheduler.alreadyRan(tooLarge.String(), "2026-06-15"))
	require.True(t, scheduler.alreadyRan(runErr.String(), "2026-06-15"))
	require.True(t, scheduler.alreadyRan(runOK.String(), "2026-06-15"))
	require.Equal(t, 1, metrics.CommunityDetectCount("too_large"))
	require.Equal(t, 1, metrics.CommunityDetectCount("error"))
	require.Equal(t, 1, metrics.CommunityDetectCount("ok"))
	require.Len(t, metrics.CommunityDetectSamples(), 3)
}

func TestCommunitySchedulerRunStoreReservesAndPrunes(t *testing.T) {
	profileID := uuid.New()
	profileSvc := &communitySchedulerProfileStub{profiles: []*domain.Profile{{ID: profileID}}}
	detector := &communitySchedulerDetectorStub{}
	store := &communitySchedulerRunStoreStub{reserved: map[string]bool{}}
	config := &communitySchedulerConfigStub{cfg: domain.CommunityDetectionRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:30",
		Timezone:       "UTC",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	}}
	scheduler := NewScheduler(detector, profileSvc, config, nil, discardCommunitySchedulerLogger(), WithSchedulerRunStore(store))
	scheduler.now = func() time.Time { return time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())
	scheduler.runDue(context.Background())

	require.Equal(t, []string{profileID.String()}, detector.profiles)
	require.Equal(t, []string{profileID.String() + "|2026-06-15", profileID.String() + "|2026-06-15"}, store.tryKeys)
	require.Equal(t, []string{"2026-06-08", "2026-06-08"}, store.pruneBefore)
}

func TestCommunitySchedulerRunStoreReservationErrorSkipsProfile(t *testing.T) {
	profileID := uuid.New()
	profileSvc := &communitySchedulerProfileStub{profiles: []*domain.Profile{{ID: profileID}}}
	detector := &communitySchedulerDetectorStub{}
	store := &communitySchedulerRunStoreStub{err: errors.New("store failed")}
	config := &communitySchedulerConfigStub{cfg: domain.CommunityDetectionRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:30",
		Timezone:       "UTC",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	}}
	scheduler := NewScheduler(detector, profileSvc, config, nil, discardCommunitySchedulerLogger(), WithSchedulerRunStore(store))
	scheduler.now = func() time.Time { return time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Empty(t, detector.profiles)
	require.Equal(t, []string{profileID.String() + "|2026-06-15"}, store.tryKeys)
}

func TestCommunitySchedulerRunProfileUsesDetectTimeout(t *testing.T) {
	var remaining time.Duration
	detector := &communitySchedulerDetectorStub{
		detectFunc: func(ctx context.Context, _ string) error {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			remaining = time.Until(deadline)
			return nil
		},
	}
	scheduler := NewScheduler(detector, &communitySchedulerProfileStub{}, &communitySchedulerConfigStub{}, nil, discardCommunitySchedulerLogger())
	now := time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }

	scheduler.runProfile(context.Background(), schedulerJob{profileID: "profile-1", runDate: "2026-06-15", dueAt: now})

	require.Greater(t, remaining, schedulerDetectTimeout-time.Second)
	require.LessOrEqual(t, remaining, schedulerDetectTimeout)
}

func TestCommunitySchedulerRunProfileReleasesRunStoreReservationOnCancellation(t *testing.T) {
	store := &communitySchedulerRunStoreStub{
		reserved: map[string]bool{"profile-1|2026-06-15": true},
	}
	detector := &communitySchedulerDetectorStub{
		detectFunc: func(ctx context.Context, _ string) error {
			return ctx.Err()
		},
	}
	scheduler := NewScheduler(
		detector,
		&communitySchedulerProfileStub{},
		&communitySchedulerConfigStub{},
		nil,
		discardCommunitySchedulerLogger(),
		WithSchedulerRunStore(store),
	)
	now := time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	scheduler.runProfile(ctx, schedulerJob{profileID: "profile-1", runDate: "2026-06-15", dueAt: now})

	require.Equal(t, []string{"profile-1|2026-06-15"}, store.releaseKeys)
	require.False(t, store.reserved["profile-1|2026-06-15"])
	require.False(t, scheduler.alreadyRan("profile-1", "2026-06-15"))
}

func TestCommunitySchedulerStopsOnProfileListError(t *testing.T) {
	profileSvc := &communitySchedulerProfileStub{err: errors.New("list failed")}
	detector := &communitySchedulerDetectorStub{}
	config := &communitySchedulerConfigStub{cfg: domain.CommunityDetectionRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "03:30",
		Timezone:       "UTC",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	}}
	scheduler := NewScheduler(detector, profileSvc, config, nil, discardCommunitySchedulerLogger())

	scheduler.runDue(context.Background())

	require.Equal(t, []int{0}, profileSvc.offsets)
	require.Empty(t, detector.profiles)
}

func TestCommunitySchedulerRunJobsBranches(t *testing.T) {
	detector := &communitySchedulerDetectorStub{}
	scheduler := NewScheduler(detector, &communitySchedulerProfileStub{}, &communitySchedulerConfigStub{}, nil, discardCommunitySchedulerLogger())
	now := time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }

	scheduler.runJobs(context.Background(), nil, 4)
	require.Empty(t, detector.profiles)

	jobs := []schedulerJob{
		{profileID: "profile-1", runDate: "2026-06-15", dueAt: now},
		{profileID: "profile-2", runDate: "2026-06-15", dueAt: now},
	}
	scheduler.runJobs(context.Background(), jobs, 0)
	require.Equal(t, []string{"profile-1", "profile-2"}, detector.profiles)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler.runJobs(ctx, jobs, 1)
	require.Equal(t, []string{"profile-1", "profile-2"}, detector.profiles)

	detector.profiles = nil
	scheduler.runJobs(context.Background(), jobs, 10)
	require.ElementsMatch(t, []string{"profile-1", "profile-2"}, detector.profiles)
}

func TestCommunitySchedulerPrunesStaleLastRunEntries(t *testing.T) {
	scheduler := NewScheduler(&communitySchedulerDetectorStub{}, &communitySchedulerProfileStub{}, &communitySchedulerConfigStub{}, nil, discardCommunitySchedulerLogger())
	scheduler.lastRun = map[string]string{
		"stale":   "2026-06-07",
		"recent":  "2026-06-14",
		"invalid": "not-a-date",
	}

	scheduler.pruneLastRun(time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC))

	require.NotContains(t, scheduler.lastRun, "stale")
	require.NotContains(t, scheduler.lastRun, "invalid")
	require.Equal(t, "2026-06-14", scheduler.lastRun["recent"])
}

func TestCommunitySchedulerHelperBranches(t *testing.T) {
	cfg := normalizeSchedulerConfig(domain.CommunityDetectionRuntimeConfig{
		JitterSeconds: -5,
	})
	require.Equal(t, "03:30", cfg.StartTimeLocal)
	require.Equal(t, "Local", cfg.Timezone)
	require.Equal(t, 1, cfg.MaxConcurrency)
	require.Equal(t, 0, cfg.JitterSeconds)

	runDate, dueAt, due := scheduledCommunityRun(time.Date(2026, 6, 15, 3, 30, 0, 0, time.UTC), "profile-1", domain.CommunityDetectionRuntimeConfig{
		Enabled:        true,
		StartTimeLocal: "bad",
		Timezone:       "not/a-zone",
		MaxConcurrency: 1,
		JitterSeconds:  0,
	})
	require.Equal(t, "2026-06-15", runDate)
	require.True(t, dueAt.IsZero())
	require.False(t, due)

	jitter := profileJitterSeconds("profile-1", "2026-06-15", 10)
	require.GreaterOrEqual(t, jitter, 0)
	require.LessOrEqual(t, jitter, 10)
	require.Equal(t, 0, profileJitterSeconds("profile-1", "2026-06-15", 0))

	require.Equal(t, "ok", communityDetectionOutcome(nil))
	require.Equal(t, "too_large", communityDetectionOutcome(ErrCommunityGraphTooLarge))
	require.Equal(t, "unavailable", communityDetectionOutcome(ErrCommunityUnavailable))
	require.Equal(t, "error", communityDetectionOutcome(errors.New("boom")))
}

type communitySchedulerProfileStub struct {
	profiles []*domain.Profile
	offsets  []int
	err      error
}

func (s *communitySchedulerProfileStub) List(_ context.Context, limit, offset int) ([]*domain.Profile, error) {
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

type communitySchedulerConfigStub struct {
	cfg domain.CommunityDetectionRuntimeConfig
	err error
}

func (s *communitySchedulerConfigStub) CommunityDetectionRuntimeConfig(context.Context) (domain.CommunityDetectionRuntimeConfig, error) {
	return s.cfg, s.err
}

type communitySchedulerDetectorStub struct {
	mu         sync.Mutex
	errs       map[string]error
	profiles   []string
	detectFunc func(ctx context.Context, profileID string) error
}

func (s *communitySchedulerDetectorStub) Detect(ctx context.Context, profileID string, _ DetectOptions) error {
	s.mu.Lock()
	s.profiles = append(s.profiles, profileID)
	err := s.errs[profileID]
	detectFunc := s.detectFunc
	s.mu.Unlock()
	if detectFunc != nil {
		return detectFunc(ctx, profileID)
	}
	return err
}

type communitySchedulerRunStoreStub struct {
	mu          sync.Mutex
	reserved    map[string]bool
	tryKeys     []string
	releaseKeys []string
	pruneBefore []string
	err         error
	releaseErr  error
	pruneErr    error
}

func (s *communitySchedulerRunStoreStub) TryMarkRun(_ context.Context, profileID, runDate string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := profileID + "|" + runDate
	s.tryKeys = append(s.tryKeys, key)
	if s.err != nil {
		return false, s.err
	}
	if s.reserved == nil {
		s.reserved = map[string]bool{}
	}
	if s.reserved[key] {
		return false, nil
	}
	s.reserved[key] = true
	return true, nil
}

func (s *communitySchedulerRunStoreStub) ReleaseRun(_ context.Context, profileID, runDate string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := profileID + "|" + runDate
	s.releaseKeys = append(s.releaseKeys, key)
	if s.releaseErr != nil {
		return s.releaseErr
	}
	if s.reserved != nil {
		delete(s.reserved, key)
	}
	return nil
}

func (s *communitySchedulerRunStoreStub) Prune(_ context.Context, beforeRunDate string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneBefore = append(s.pruneBefore, beforeRunDate)
	return s.pruneErr
}

func discardCommunitySchedulerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
