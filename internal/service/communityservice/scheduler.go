package communityservice

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
)

const schedulerPageSize = 100

type Scheduler struct {
	service Service
	teams   TeamService
	config  AppConfig
	logger  *slog.Logger
	now     func() time.Time
	runMu   sync.Mutex
}

func NewScheduler(service Service, teams TeamService, config AppConfig, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{service: service, teams: teams, config: config, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.service == nil || s.teams == nil {
		return
	}
	timer := time.NewTimer(nextMinuteDelay(s.now()))
	defer timer.Stop()
	if !s.waitForMinuteBoundary(ctx, timer.C) {
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

func (s *Scheduler) runDue(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	var runs sync.WaitGroup
	defer runs.Wait()

	now := s.now().UTC()
	if s.config == nil {
		return
	}
	cfg, configErr := s.config.CommunityDetectionRuntimeConfig(ctx)
	if configErr != nil || !cfg.Enabled || !dueAt(now, runtimeConfig{Enabled: cfg.Enabled, StartTimeLocal: cfg.StartTimeLocal, Timezone: cfg.Timezone}) {
		return
	}
	concurrency := cfg.MaxConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 8 {
		concurrency = 8
	}
	semaphore := make(chan struct{}, concurrency)
	for offset := 0; ; offset += schedulerPageSize {
		teams, err := s.teams.List(ctx, schedulerPageSize, offset)
		if err != nil {
			s.logger.Warn("community scheduler: team listing failed", slog.String("error_kind", "team_list_failed"))
			return
		}
		for _, team := range teams {
			if team == nil || team.ID == uuid.Nil {
				continue
			}
			teamID := team.ID.String()
			runs.Add(1)
			go func(teamID string) {
				defer runs.Done()
				delay := deterministicJitter(teamID, now, cfg.JitterSeconds)
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				select {
				case semaphore <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-semaphore }()
				if _, runErr := s.service.RunScheduled(ctx, teamID, now); runErr != nil {
					s.logger.Warn("community scheduler: run failed", slog.String("error_kind", "run_failed"))
				}
			}(teamID)
		}
		if len(teams) < schedulerPageSize {
			return
		}
	}
}

type runtimeConfig struct {
	Enabled        bool
	StartTimeLocal string
	Timezone       string
}

func dueAt(now time.Time, cfg runtimeConfig) bool {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	var hour, minute int
	if _, err := time.Parse("15:04", cfg.StartTimeLocal); err != nil {
		hour, minute = 3, 0
	} else {
		parsed, _ := time.Parse("15:04", cfg.StartTimeLocal)
		hour, minute = parsed.Hour(), parsed.Minute()
	}
	return local.Hour() == hour && local.Minute() == minute
}

func nextMinuteDelay(now time.Time) time.Duration {
	next := now.UTC().Truncate(time.Minute).Add(time.Minute)
	return next.Sub(now)
}

func deterministicJitter(teamID string, window time.Time, maxSeconds int) time.Duration {
	if maxSeconds <= 0 {
		return 0
	}
	maxDurationSeconds := int(math.MaxInt64 / int64(time.Second))
	if maxSeconds > maxDurationSeconds {
		maxSeconds = maxDurationSeconds
	}
	digest := sha256.Sum256([]byte(teamID + "\x00" + window.UTC().Format("2006-01-02")))
	value := uint64(binary.BigEndian.Uint32(digest[:4])) % uint64(maxSeconds+1)
	return time.Duration(value) * time.Second
}
