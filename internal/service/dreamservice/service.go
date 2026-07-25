package dreamservice

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const lockTimeout = 30 * time.Second

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
		deps.Generator = NewHeuristicGenerator("")
	}
	return &service{deps: deps, now: now}
}

func (s *service) RunCycle(ctx context.Context, _ string, req RunCycleRequest) (*RunCycleResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("dreaming cycle: dream repository is required")
	}
	return s.runCycle(ctx, req)
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

func parseProfileID(profileID string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(profileID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("dreaming config: invalid profile id: %w", err)
	}
	return parsed, nil
}
