package dreamservice

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	schedulerTeamPageSize     = 100
	schedulerLastRunRetention = 7 * 24 * time.Hour
	schedulerRunDateLayout    = "2006-01-02"
)

type scheduledWindowState uint8

const (
	scheduledWindowNotDue scheduledWindowState = iota
	scheduledWindowDue
	scheduledWindowMissed
)

type scheduledWindow struct {
	at     time.Time
	year   int
	month  time.Month
	day    int
	hour   int
	minute int
}

type scheduledWindowArm struct {
	runDate        string
	startTimeLocal string
	timezone       string
}

type Scheduler struct {
	service Service
	teams   TeamService
	now     func() time.Time
	logger  *slog.Logger

	mu       sync.Mutex
	observed map[string]string
	armed    map[string]scheduledWindowArm
}

type scheduledRecoveryService interface {
	RecoverScheduledCycle(ctx context.Context, teamID string) (*RunCycleResult, error)
}

func NewScheduler(service Service, teams TeamService, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		service:  service,
		teams:    teams,
		now:      func() time.Time { return time.Now().UTC() },
		logger:   logger,
		observed: map[string]string{},
		armed:    map[string]scheduledWindowArm{},
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.service == nil || s.teams == nil {
		return
	}
	s.runDue(ctx)
	minuteTimer := time.NewTimer(schedulerNextMinuteDelay(time.Now().UTC()))
	defer minuteTimer.Stop()
	if !s.waitForMinuteBoundary(ctx, minuteTimer.C) {
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

func (s *Scheduler) waitForMinuteBoundary(ctx context.Context, boundary <-chan time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-boundary:
		s.runDue(ctx)
		return true
	}
}

func schedulerNextMinuteDelay(now time.Time) time.Duration {
	next := now.UTC().Truncate(time.Minute).Add(time.Minute)
	return next.Sub(now)
}

func (s *Scheduler) runDue(ctx context.Context) {
	now := s.now()
	s.pruneObserved(now)
	for offset := 0; ; offset += schedulerTeamPageSize {
		teams, err := s.teams.List(ctx, schedulerTeamPageSize, offset)
		if err != nil {
			s.logger.Warn("dreaming scheduler: list teams failed", slog.Int("offset", offset), slog.String("error_kind", "team_list_failed"))
			return
		}
		for _, team := range teams {
			if team == nil {
				continue
			}
			teamID := team.ID.String()
			cfg, err := s.service.EffectiveConfig(ctx, teamID)
			if err != nil {
				s.logger.Warn("dreaming scheduler: config resolve failed", slog.String("team_id", teamID), slog.String("error_kind", "config_resolve_failed"))
				continue
			}
			if recovery, ok := s.service.(scheduledRecoveryService); ok {
				recovered, recoverErr := recovery.RecoverScheduledCycle(ctx, teamID)
				if recoverErr != nil {
					s.logger.Warn("dreaming scheduler: recovery failed", slog.String("team_id", teamID), slog.String("error_kind", "recovery_failed"))
				} else if recovered != nil {
					s.logger.Info("dreaming scheduler: expired cycle recovered",
						slog.String("team_id", teamID),
						slog.String("run_id", recovered.RunID),
						slog.String("status", recovered.Status),
					)
				}
			}
			if !cfg.Enabled {
				s.disarm(teamID)
				continue
			}
			window, ok := scheduledWindowFor(now, cfg)
			if !ok {
				s.logger.Warn("dreaming scheduler: schedule unavailable", slog.String("team_id", teamID), slog.String("error_kind", "schedule_unavailable"))
				continue
			}
			state := scheduledWindowAt(now, cfg)
			runDate := now.In(window.at.Location()).Format(schedulerRunDateLayout)
			arm := scheduledWindowArm{
				runDate:        runDate,
				startTimeLocal: cfg.StartTimeLocal,
				timezone:       cfg.Timezone,
			}
			if state == scheduledWindowNotDue {
				s.markArmed(teamID, arm)
				continue
			}
			if s.alreadyObserved(teamID, runDate) {
				s.disarmMatching(teamID, arm)
				continue
			}
			if state == scheduledWindowDue {
				s.markArmed(teamID, arm)
			} else if !s.isArmed(teamID, arm) {
				continue
			}
			var result *RunCycleResult
			if state == scheduledWindowDue {
				result, err = s.service.RunScheduledCycle(ctx, teamID, window.at)
			} else {
				result, err = s.service.RecordMissedScheduledCycle(ctx, teamID, runDate)
			}
			if err != nil {
				s.logger.Warn("dreaming scheduler: cycle failed", slog.String("team_id", teamID), slog.String("error_kind", "cycle_failed"))
				continue
			}
			if result.Status == "skipped" {
				s.logger.Warn("dreaming scheduler: cycle skipped before it could claim the window", slog.String("team_id", teamID), slog.String("run_date", runDate))
				continue
			}
			s.markObserved(teamID, runDate)
			s.logger.Info("dreaming scheduler: cycle observed",
				slog.String("team_id", teamID),
				slog.String("run_id", result.RunID),
				slog.String("run_date", runDate),
				slog.String("status", result.Status),
			)
		}
		if len(teams) < schedulerTeamPageSize {
			return
		}
	}
}

func isDueAt(now time.Time, cfg EffectiveConfig) bool {
	return scheduledWindowAt(now, cfg) == scheduledWindowDue
}

func scheduledWindowAt(now time.Time, cfg EffectiveConfig) scheduledWindowState {
	window, ok := scheduledWindowFor(now, cfg)
	if !ok {
		return scheduledWindowNotDue
	}
	local := now.In(window.at.Location())
	if window.exists() && local.Hour() == window.hour && local.Minute() == window.minute {
		return scheduledWindowDue
	}
	if local.Hour() > window.hour || (local.Hour() == window.hour && local.Minute() > window.minute) {
		return scheduledWindowMissed
	}
	return scheduledWindowNotDue
}

func scheduledWindowAtTime(now time.Time, cfg EffectiveConfig) (time.Time, bool) {
	window, ok := scheduledWindowFor(now, cfg)
	if !ok {
		return time.Time{}, false
	}
	return window.at, true
}

func scheduledWindowFor(now time.Time, cfg EffectiveConfig) (scheduledWindow, bool) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return scheduledWindow{}, false
	}
	start, err := time.Parse("15:04", cfg.StartTimeLocal)
	if err != nil {
		return scheduledWindow{}, false
	}
	local := now.In(loc)
	return scheduledWindow{
		at:     time.Date(local.Year(), local.Month(), local.Day(), start.Hour(), start.Minute(), 0, 0, loc),
		year:   local.Year(),
		month:  local.Month(),
		day:    local.Day(),
		hour:   start.Hour(),
		minute: start.Minute(),
	}, true
}

func (w scheduledWindow) exists() bool {
	actual := w.at.In(w.at.Location())
	return actual.Year() == w.year &&
		actual.Month() == w.month &&
		actual.Day() == w.day &&
		actual.Hour() == w.hour &&
		actual.Minute() == w.minute
}

func (s *Scheduler) alreadyObserved(teamID, runDate string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observed[teamID] == runDate
}

func (s *Scheduler) markObserved(teamID, runDate string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed[teamID] = runDate
	delete(s.armed, teamID)
}

func (s *Scheduler) markArmed(teamID string, arm scheduledWindowArm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed[teamID] = arm
}

func (s *Scheduler) isArmed(teamID string, arm scheduledWindowArm) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.armed[teamID] == arm
}

func (s *Scheduler) disarm(teamID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.armed, teamID)
}

func (s *Scheduler) disarmMatching(teamID string, arm scheduledWindowArm) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed[teamID] == arm {
		delete(s.armed, teamID)
	}
}

func (s *Scheduler) pruneObserved(now time.Time) {
	cutoffDate := now.UTC().Add(-schedulerLastRunRetention).Format(schedulerRunDateLayout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for teamID, runDate := range s.observed {
		if _, err := time.Parse(schedulerRunDateLayout, runDate); err != nil || runDate < cutoffDate {
			delete(s.observed, teamID)
		}
	}
	for teamID, arm := range s.armed {
		if _, err := time.Parse(schedulerRunDateLayout, arm.runDate); err != nil || arm.runDate < cutoffDate {
			delete(s.armed, teamID)
		}
	}
}
