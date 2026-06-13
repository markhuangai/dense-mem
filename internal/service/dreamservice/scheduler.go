package dreamservice

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	schedulerProfilePageSize  = 100
	schedulerLastRunRetention = 7 * 24 * time.Hour
	schedulerRunDateLayout    = "2006-01-02"
)

type Scheduler struct {
	service  Service
	profiles ProfileService
	now      func() time.Time
	logger   *slog.Logger

	mu      sync.Mutex
	lastRun map[string]string
}

func NewScheduler(service Service, profiles ProfileService, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		service:  service,
		profiles: profiles,
		now:      func() time.Time { return time.Now().UTC() },
		logger:   logger,
		lastRun:  map[string]string{},
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.service == nil || s.profiles == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
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
	for offset := 0; ; offset += schedulerProfilePageSize {
		profiles, err := s.profiles.List(ctx, schedulerProfilePageSize, offset)
		if err != nil {
			s.logger.Warn("dreaming scheduler: list teams failed", slog.Int("offset", offset), slog.String("error", err.Error()))
			return
		}
		for _, profile := range profiles {
			if profile == nil {
				continue
			}
			profileID := profile.ID.String()
			cfg, err := s.service.EffectiveConfig(ctx, profileID)
			if err != nil {
				s.logger.Warn("dreaming scheduler: config resolve failed", slog.String("team_id", profileID), slog.String("error", err.Error()))
				continue
			}
			if !cfg.Enabled || (!cfg.ReflectEnabled && !cfg.ReevaluateEnabled && !cfg.DreamEnabled) {
				continue
			}
			if !isDueAt(now, cfg) {
				continue
			}
			runDate := localRunDate(now, cfg)
			if s.alreadyRan(profileID, runDate) {
				continue
			}
			result, err := s.service.RunCycle(ctx, profileID, RunCycleRequest{})
			if err != nil {
				s.logger.Warn("dreaming scheduler: cycle failed", slog.String("team_id", profileID), slog.String("error", err.Error()))
				continue
			}
			s.markRan(profileID, runDate)
			s.logger.Info("dreaming scheduler: cycle completed", slog.String("team_id", profileID), slog.String("run_id", result.RunID), slog.String("run_date", runDate))
		}
		if len(profiles) < schedulerProfilePageSize {
			return
		}
	}
}

func isDueAt(now time.Time, cfg EffectiveConfig) bool {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	start, err := time.Parse("15:04", cfg.StartTimeLocal)
	if err != nil {
		return false
	}
	return local.Hour() == start.Hour() && local.Minute() == start.Minute()
}

func (s *Scheduler) alreadyRan(profileID, runDate string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRun[profileID] == runDate
}

func (s *Scheduler) markRan(profileID, runDate string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRun[profileID] = runDate
}

func (s *Scheduler) pruneLastRun(now time.Time) {
	cutoffDate := now.UTC().Add(-schedulerLastRunRetention).Format(schedulerRunDateLayout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for profileID, runDate := range s.lastRun {
		if _, err := time.Parse(schedulerRunDateLayout, runDate); err != nil || runDate < cutoffDate {
			delete(s.lastRun, profileID)
		}
	}
}
