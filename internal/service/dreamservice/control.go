package dreamservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type ControlDependencies struct {
	Store     repository.DreamControlRepository
	AppConfig AppConfig
	Teams     TeamConfigService
}

type ControlService interface {
	List(ctx context.Context, teamID string, opts ListOptions) ([]*domain.Dream, string, error)
	Get(ctx context.Context, teamID, dreamID string) (*domain.Dream, error)
	ListRuns(ctx context.Context, teamID string, limit int) ([]*RunCycleResult, error)
	Status(ctx context.Context, teamID string) (*StatusResult, error)
}

type controlService struct {
	deps ControlDependencies
}

var _ ControlService = (*controlService)(nil)

func NewControl(deps ControlDependencies) ControlService {
	return &controlService{deps: deps}
}

func (s *controlService) List(ctx context.Context, teamID string, opts ListOptions) ([]*domain.Dream, string, error) {
	if s.deps.Store == nil {
		return nil, "", fmt.Errorf("control dream list: dream repository is required")
	}
	records, next, err := s.deps.Store.ListHypotheses(ctx, repository.ListHypothesesInput{
		TeamID:    teamID,
		Status:    opts.Status,
		Limit:     opts.Limit,
		Cursor:    opts.Cursor,
		Sort:      opts.Sort,
		Direction: opts.Direction,
	})
	if err != nil {
		return nil, "", translateDreamRepositoryError(err)
	}
	return dreamRecords(records), next, nil
}

func (s *controlService) Get(ctx context.Context, teamID, dreamID string) (*domain.Dream, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("control dream get: dream repository is required")
	}
	record, err := s.deps.Store.GetHypothesis(ctx, repository.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, translateDreamRepositoryError(err)
	}
	return dreamRecord(record), nil
}

func (s *controlService) ListRuns(ctx context.Context, teamID string, limit int) ([]*RunCycleResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("control dream cycle runs: dream repository is required")
	}
	runs, err := s.deps.Store.ListDreamCyclesForTeam(ctx, teamID, limit)
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	results := make([]*RunCycleResult, 0, len(runs))
	for i := range runs {
		results = append(results, cycleRunResult(&runs[i]))
	}
	return results, nil
}

func (s *controlService) Status(ctx context.Context, teamID string) (*StatusResult, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("control dream status: dream repository is required")
	}
	cfg, err := resolveEffectiveTeamConfig(ctx, teamID, s.deps.AppConfig, s.deps.Teams)
	if err != nil {
		return nil, err
	}
	pending, err := s.deps.Store.CountHypotheses(ctx, teamID, string(domain.DreamStatusProposed))
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	runs, err := s.deps.Store.ListDreamCyclesForTeam(ctx, teamID, 1)
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	var latest *RunCycleResult
	if len(runs) > 0 {
		latest = cycleRunResult(&runs[0])
	}
	return &StatusResult{EffectiveConfig: cfg, LatestRun: latest, PendingCount: pending}, nil
}
