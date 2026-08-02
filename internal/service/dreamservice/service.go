package dreamservice

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	manualDreamCycleLease     = 30 * time.Second
	scheduledDreamCycleLease  = 15 * time.Minute
	scheduledRecoveryAttempts = 3
)

type service struct {
	deps Dependencies
	now  func() time.Time
}

var _ Service = (*service)(nil)

func New(deps Dependencies) Service {
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if deps.Generator == nil {
		deps.Generator = unavailableGenerator{}
	}
	if deps.ScheduledStore == nil {
		if store, ok := deps.Store.(repository.ScheduledDreamRepository); ok {
			deps.ScheduledStore = store
		}
	}
	return &service{deps: deps, now: now}
}

func (s *service) cycleLease(scheduled bool) time.Duration {
	lease := manualDreamCycleLease
	if scheduled {
		lease = scheduledDreamCycleLease
	}
	if s.deps.ProviderCycleLease > lease {
		return s.deps.ProviderCycleLease
	}
	return lease
}

func (s *service) RunCycle(ctx context.Context, _ string, req RunCycleRequest) (*RunCycleResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dreaming cycle: dream repository is required")
	}
	return s.runCycle(ctx, req)
}

func (s *service) RunScheduledCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error) {
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("scheduled dreaming cycle: scheduled dream repository is required")
	}
	return s.runScheduledCycle(ctx, teamID, windowAt)
}

func (s *service) RecoverScheduledCycle(ctx context.Context, teamID string) (*RunCycleResult, error) {
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("recover scheduled dreaming cycle: scheduled dream repository is required")
	}
	return s.recoverScheduledCycle(ctx, teamID)
}

func (s *service) RecordMissedScheduledCycle(ctx context.Context, teamID, runDate string) (*RunCycleResult, error) {
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("record missed scheduled dreaming cycle: scheduled dream repository is required")
	}
	return s.recordMissedScheduledCycle(ctx, teamID, runDate)
}

func (s *service) List(ctx context.Context, _ string, opts ListOptions) ([]*domain.Dream, string, error) {
	if s.deps.Store == nil {
		return nil, "", fmt.Errorf("dream list: dream repository is required")
	}
	return s.listDreams(ctx, opts)
}

func (s *service) Get(ctx context.Context, _ string, dreamID string) (*domain.Dream, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream get: dream repository is required")
	}
	return s.getDream(ctx, dreamID)
}

func (s *service) ListRuns(ctx context.Context, _ string, limit int) ([]*RunCycleResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream cycle runs: dream repository is required")
	}
	return s.listRuns(ctx, limit)
}

func (s *service) Recall(ctx context.Context, _ string, query string, limit int) ([]*domain.Dream, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream recall: dream repository is required")
	}
	return s.recallDreams(ctx, query, limit)
}

func (s *service) ResolveFeedback(ctx context.Context, _ string, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("resolve dream feedback: dream repository is required")
	}
	return s.resolveFeedback(ctx, req)
}

func (s *service) Status(ctx context.Context, _ string) (*StatusResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dream status: dream repository is required")
	}
	return s.status(ctx)
}

func (s *service) recordDreamFeedback(ctx context.Context, decision string, dream *domain.Dream, outcome string) {
	fromStatus := ""
	if dream != nil {
		fromStatus = string(dream.Status)
	}
	observability.RecordDreamFeedback(ctx, s.deps.Metrics, observability.DreamFeedback{
		Decision:   decision,
		Outcome:    outcome,
		FromStatus: fromStatus,
	})
}

func localRunDate(now time.Time, cfg EffectiveConfig) string {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return now.In(loc).Format("2006-01-02")
}

func parseTeamID(teamID string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(teamID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("dreaming config: invalid team id: %w", err)
	}
	return parsed, nil
}
