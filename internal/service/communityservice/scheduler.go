package communityservice

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	schedulerProfilePageSize  = 100
	schedulerDetectTimeout    = 10 * time.Minute
	schedulerLastRunRetention = 7 * 24 * time.Hour
	schedulerRunDateLayout    = "2006-01-02"
	schedulerTickInterval     = time.Minute
)

type SchedulerProfileService interface {
	List(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
}

type SchedulerAppConfig interface {
	CommunityDetectionRuntimeConfig(ctx context.Context) (domain.CommunityDetectionRuntimeConfig, error)
}

type SchedulerRunStore interface {
	TryMarkRun(ctx context.Context, profileID, runDate string) (bool, error)
	Prune(ctx context.Context, beforeRunDate string) error
}

type SchedulerOption func(*Scheduler)

func WithSchedulerRunStore(store SchedulerRunStore) SchedulerOption {
	return func(s *Scheduler) {
		s.runStore = store
	}
}

type Scheduler struct {
	detector  DetectCommunityService
	profiles  SchedulerProfileService
	appConfig SchedulerAppConfig
	metrics   observability.DiscoverabilityMetrics
	runStore  SchedulerRunStore
	now       func() time.Time
	logger    *slog.Logger

	mu      sync.Mutex
	lastRun map[string]string
}

type schedulerJob struct {
	profileID string
	runDate   string
	dueAt     time.Time
}

func NewScheduler(detector DetectCommunityService, profiles SchedulerProfileService, appConfig SchedulerAppConfig, metrics observability.DiscoverabilityMetrics, logger *slog.Logger, options ...SchedulerOption) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	scheduler := &Scheduler{
		detector:  detector,
		profiles:  profiles,
		appConfig: appConfig,
		metrics:   metrics,
		now:       func() time.Time { return time.Now() },
		logger:    logger,
		lastRun:   map[string]string{},
	}
	for _, option := range options {
		if option != nil {
			option(scheduler)
		}
	}
	return scheduler
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.detector == nil || s.profiles == nil || s.appConfig == nil {
		return
	}
	ticker := time.NewTicker(schedulerTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDue(ctx)
		}
	}
}

func (s *Scheduler) runDue(ctx context.Context) {
	now := s.now()
	s.pruneLastRun(now)
	s.pruneRunStore(ctx, now)

	cfg, err := s.appConfig.CommunityDetectionRuntimeConfig(ctx)
	if err != nil {
		s.logger.Warn("community scheduler: config resolve failed", slog.String("error", err.Error()))
		return
	}
	cfg = normalizeSchedulerConfig(cfg)
	if !cfg.Enabled {
		return
	}

	jobs := make([]schedulerJob, 0)
	for offset := 0; ; offset += schedulerProfilePageSize {
		if err := ctx.Err(); err != nil {
			return
		}
		profiles, err := s.profiles.List(ctx, schedulerProfilePageSize, offset)
		if err != nil {
			s.logger.Warn("community scheduler: list teams failed", slog.Int("offset", offset), slog.String("error", err.Error()))
			return
		}
		for _, profile := range profiles {
			if profile == nil {
				continue
			}
			profileID := profile.ID.String()
			runDate, dueAt, due := scheduledCommunityRun(now, profileID, cfg)
			if !due || !s.reserveRun(ctx, profileID, runDate) {
				continue
			}
			jobs = append(jobs, schedulerJob{profileID: profileID, runDate: runDate, dueAt: dueAt})
		}
		if len(profiles) < schedulerProfilePageSize {
			break
		}
	}

	s.runJobs(ctx, jobs, cfg.MaxConcurrency)
}

func (s *Scheduler) runJobs(ctx context.Context, jobs []schedulerJob, maxConcurrency int) {
	if len(jobs) == 0 {
		return
	}
	if maxConcurrency <= 1 {
		for _, job := range jobs {
			if err := ctx.Err(); err != nil {
				return
			}
			s.runProfile(ctx, job)
		}
		return
	}
	if maxConcurrency > len(jobs) {
		maxConcurrency = len(jobs)
	}

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(job schedulerJob) {
			defer wg.Done()
			defer func() { <-sem }()
			s.runProfile(ctx, job)
		}(job)
	}
	wg.Wait()
}

func (s *Scheduler) runProfile(ctx context.Context, job schedulerJob) {
	started := s.now()
	detectCtx, cancel := context.WithTimeout(ctx, schedulerDetectTimeout)
	defer cancel()
	err := s.detector.Detect(detectCtx, job.profileID, DetectOptions{})
	duration := s.now().Sub(started)
	if err != nil {
		if detectCtx.Err() != nil {
			s.logger.Warn("community scheduler: detection canceled",
				slog.String("profile_id", job.profileID),
				slog.String("run_date", job.runDate),
				slog.String("context_error", detectCtx.Err().Error()),
				slog.String("error", err.Error()),
			)
			return
		}
		s.markRan(job.profileID, job.runDate)
		outcome := communityDetectionOutcome(err)
		s.metrics.IncCommunityDetect(outcome)
		s.metrics.ObserveCommunityDetect(duration.Seconds(), 0)
		logFn := s.logger.Warn
		if errors.Is(err, ErrCommunityGraphTooLarge) {
			logFn = s.logger.Info
		}
		logFn("community scheduler: detection skipped or failed",
			slog.String("profile_id", job.profileID),
			slog.String("run_date", job.runDate),
			slog.String("outcome", outcome),
			slog.String("due_at", job.dueAt.Format(time.RFC3339)),
			slog.Duration("duration", duration),
			slog.String("error", err.Error()),
		)
		return
	}

	s.markRan(job.profileID, job.runDate)
	s.metrics.IncCommunityDetect("ok")
	s.metrics.ObserveCommunityDetect(duration.Seconds(), 0)
	s.logger.Info("community scheduler: detection completed",
		slog.String("profile_id", job.profileID),
		slog.String("run_date", job.runDate),
		slog.String("due_at", job.dueAt.Format(time.RFC3339)),
		slog.Duration("duration", duration),
	)
}

func normalizeSchedulerConfig(cfg domain.CommunityDetectionRuntimeConfig) domain.CommunityDetectionRuntimeConfig {
	if cfg.StartTimeLocal == "" {
		cfg.StartTimeLocal = "03:30"
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Local"
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	if cfg.JitterSeconds < 0 {
		cfg.JitterSeconds = 0
	}
	return cfg
}

func scheduledCommunityRun(now time.Time, profileID string, cfg domain.CommunityDetectionRuntimeConfig) (string, time.Time, bool) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.Local
	}
	local := now.In(loc)
	start, err := time.Parse("15:04", cfg.StartTimeLocal)
	if err != nil {
		return local.Format(schedulerRunDateLayout), time.Time{}, false
	}
	runDate := local.Format(schedulerRunDateLayout)
	dueAt := time.Date(local.Year(), local.Month(), local.Day(), start.Hour(), start.Minute(), 0, 0, loc)
	if cfg.JitterSeconds > 0 {
		dueAt = dueAt.Add(time.Duration(profileJitterSeconds(profileID, runDate, cfg.JitterSeconds)) * time.Second)
	}
	return runDate, dueAt, !local.Before(dueAt)
}

func profileJitterSeconds(profileID, runDate string, maxSeconds int) int {
	if maxSeconds <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(profileID))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(runDate))
	return int(h.Sum32() % uint32(maxSeconds+1))
}

func communityDetectionOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrCommunityGraphTooLarge):
		return "too_large"
	case errors.Is(err, ErrCommunityUnavailable):
		return "unavailable"
	default:
		return "error"
	}
}

func (s *Scheduler) alreadyRan(profileID, runDate string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRun[profileID] == runDate
}

func (s *Scheduler) reserveRun(ctx context.Context, profileID, runDate string) bool {
	if s.runStore == nil {
		return !s.alreadyRan(profileID, runDate)
	}
	reserved, err := s.runStore.TryMarkRun(ctx, profileID, runDate)
	if err != nil {
		s.logger.Warn("community scheduler: run reservation failed",
			slog.String("profile_id", profileID),
			slog.String("run_date", runDate),
			slog.String("error", err.Error()),
		)
		return false
	}
	return reserved
}

func (s *Scheduler) markRan(profileID, runDate string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRun[profileID] = runDate
}

func (s *Scheduler) pruneRunStore(ctx context.Context, now time.Time) {
	if s.runStore == nil {
		return
	}
	cutoffDate := now.Add(-schedulerLastRunRetention).Format(schedulerRunDateLayout)
	if err := s.runStore.Prune(ctx, cutoffDate); err != nil {
		s.logger.Warn("community scheduler: run store prune failed",
			slog.String("before_run_date", cutoffDate),
			slog.String("error", err.Error()),
		)
	}
}

func (s *Scheduler) pruneLastRun(now time.Time) {
	cutoffDate := now.Add(-schedulerLastRunRetention).Format(schedulerRunDateLayout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for profileID, runDate := range s.lastRun {
		if _, err := time.Parse(schedulerRunDateLayout, runDate); err != nil || runDate < cutoffDate {
			delete(s.lastRun, profileID)
		}
	}
}
