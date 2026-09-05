package dreamservice

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
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

type schedulerTeamState struct {
	team   *domain.Team
	teamID string
	cfg    EffectiveConfig
}

type Scheduler struct {
	service Service
	teams   TeamService
	now     func() time.Time
	logger  *slog.Logger

	mu             sync.Mutex
	observed       map[string]string
	armed          map[string]scheduledWindowArm
	hourlyObserved map[string]string
}

type scheduledRecoveryService interface {
	RecoverScheduledCycle(ctx context.Context, teamID string) (*RunCycleResult, error)
}

type evidenceScheduledService interface {
	RunScheduledEvidenceCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error)
	RecoverScheduledEvidenceCycle(ctx context.Context, teamID string) (*RunCycleResult, error)
}

func NewScheduler(service Service, teams TeamService, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		service:        service,
		teams:          teams,
		now:            func() time.Time { return time.Now().UTC() },
		logger:         logger,
		observed:       map[string]string{},
		armed:          map[string]scheduledWindowArm{},
		hourlyObserved: map[string]string{},
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
	teamStates := make([]schedulerTeamState, 0, schedulerTeamPageSize)
	runDaily := func(teamState schedulerTeamState) {
		teamID, cfg := teamState.teamID, teamState.cfg
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
			return
		}
		window, ok := scheduledWindowFor(now, cfg)
		if !ok {
			s.logger.Warn("dreaming scheduler: schedule unavailable", slog.String("team_id", teamID), slog.String("error_kind", "schedule_unavailable"))
			return
		}
		state := scheduledWindowAt(now, cfg)
		runDate := now.In(window.at.Location()).Format(schedulerRunDateLayout)
		arm := scheduledWindowArm{runDate: runDate, startTimeLocal: cfg.StartTimeLocal, timezone: cfg.Timezone}
		switch {
		case state == scheduledWindowNotDue:
			s.markArmed(teamID, arm)
		case s.alreadyObserved(teamID, runDate):
			s.disarmMatching(teamID, arm)
		default:
			if state == scheduledWindowDue {
				s.markArmed(teamID, arm)
			}
			if state != scheduledWindowDue && !s.isArmed(teamID, arm) {
				return
			}
			var result *RunCycleResult
			var cycleErr error
			if state == scheduledWindowDue {
				result, cycleErr = s.service.RunScheduledCycle(ctx, teamID, window.at)
			} else {
				result, cycleErr = s.service.RecordMissedScheduledCycle(ctx, teamID, runDate)
			}
			if cycleErr != nil {
				s.logger.Warn("dreaming scheduler: cycle failed", slog.String("team_id", teamID), slog.String("error_kind", "cycle_failed"))
			} else if result == nil || result.Status == "skipped" {
				s.logger.Warn("dreaming scheduler: cycle skipped before it could claim the window", slog.String("team_id", teamID), slog.String("run_date", runDate))
			} else {
				s.markObserved(teamID, runDate)
				s.logger.Info("dreaming scheduler: cycle observed",
					slog.String("team_id", teamID), slog.String("run_id", result.RunID),
					slog.String("run_date", runDate), slog.String("status", result.Status))
			}
		}
	}
	for offset := 0; ; offset += schedulerTeamPageSize {
		teams, err := s.teams.List(ctx, schedulerTeamPageSize, offset)
		if err != nil {
			s.logger.Warn("dreaming scheduler: list teams failed", slog.Int("offset", offset), slog.String("error_kind", "team_list_failed"))
			return
		}
		pageStates := make([]schedulerTeamState, 0, len(teams))
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
			state := schedulerTeamState{team: team, teamID: teamID, cfg: cfg}
			teamStates = append(teamStates, state)
			pageStates = append(pageStates, state)
		}
		for _, state := range pageStates {
			runDaily(state)
		}
		if len(teams) < schedulerTeamPageSize {
			break
		}
	}

	for _, teamState := range teamStates {
		if !isActiveScheduledTeam(teamState.team) {
			continue
		}
		teamID, cfg := teamState.teamID, teamState.cfg
		if recovery, ok := s.service.(evidenceScheduledService); ok {
			recovered, recoverErr := recovery.RecoverScheduledEvidenceCycle(ctx, teamID)
			if recoverErr != nil {
				s.logger.Warn("dreaming scheduler: evidence recovery failed", slog.String("team_id", teamID), slog.String("error_kind", "evidence_recovery_failed"))
			} else if recovered != nil {
				s.logger.Info("dreaming scheduler: expired evidence cycle recovered",
					slog.String("team_id", teamID), slog.String("run_id", recovered.RunID), slog.String("status", recovered.Status))
			}
		}
		s.runEvidenceDue(ctx, teamID, cfg, now)
	}
}

func isActiveScheduledTeam(team *domain.Team) bool {
	if team == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(team.Status))
	return status == "" || status == "active"
}

func (s *Scheduler) runEvidenceDue(ctx context.Context, teamID string, cfg EffectiveConfig, now time.Time) {
	service, ok := s.service.(evidenceScheduledService)
	if !ok || !cfg.Enabled {
		return
	}
	windowAt := now.UTC().Truncate(time.Hour)
	windowKey := evidenceDiscoveryWindowKey(windowAt)
	if s.hourlyAlreadyObserved(teamID, windowKey) {
		return
	}
	result, err := service.RunScheduledEvidenceCycle(ctx, teamID, windowAt)
	if err != nil {
		if result != nil && result.durablyFinalized {
			s.markHourlyObserved(teamID, windowKey)
			s.logger.Info("dreaming scheduler: evidence cycle observed",
				slog.String("team_id", teamID), slog.String("run_id", result.RunID),
				slog.String("window_key", windowKey), slog.String("status", result.Status))
		}
		s.logger.Warn("dreaming scheduler: evidence cycle failed", slog.String("team_id", teamID), slog.String("error_kind", "evidence_cycle_failed"))
		return
	}
	if result == nil || result.Status == "skipped" {
		return
	}
	s.markHourlyObserved(teamID, windowKey)
	s.logger.Info("dreaming scheduler: evidence cycle observed",
		slog.String("team_id", teamID), slog.String("run_id", result.RunID),
		slog.String("window_key", windowKey), slog.String("status", result.Status))
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

func (s *Scheduler) hourlyAlreadyObserved(teamID, windowKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hourlyObserved[teamID] == windowKey
}

func (s *Scheduler) markHourlyObserved(teamID, windowKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hourlyObserved[teamID] = windowKey
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
	for teamID, windowKey := range s.hourlyObserved {
		if len(windowKey) < len("hour:2006-01-02T15") || windowKey < "hour:"+cutoffDate+"T00" {
			delete(s.hourlyObserved, teamID)
		}
	}
}
