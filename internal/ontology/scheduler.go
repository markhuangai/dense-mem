package ontology

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type MaintenanceScheduler struct {
	service *MaintenanceService
	logger  *slog.Logger
}

func NewMaintenanceScheduler(service *MaintenanceService, logger *slog.Logger) *MaintenanceScheduler {
	return &MaintenanceScheduler{service: service, logger: logger}
}

func (s *MaintenanceScheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.run(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *MaintenanceScheduler) run(ctx context.Context) {
	policy, err := s.service.policy(ctx)
	if err != nil {
		s.logFailure("configuration_unavailable")
		return
	}
	if !policy.Enabled {
		return
	}
	window, err := s.service.deps.Repository.EnsureMaintenanceWindow(ctx, policy, s.service.deps.Now())
	if err != nil {
		s.logFailure("window_unavailable")
		return
	}
	if window == nil {
		return
	}
	var workers sync.WaitGroup
	for range window.Policy.MaxConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if s.service.deps.Projections != nil {
				status, err := s.service.deps.Repository.MaintenanceStatus(ctx, s.service.deps.Now())
				if err != nil {
					s.logFailure("projection_status_unavailable")
					return
				}
				if status.Paused {
					return
				}
				for range 20 {
					progress, err := s.service.deps.Projections.RunProjectionTurn(ctx)
					if err != nil {
						s.logFailure("topic_projection_failed")
						break
					}
					if !progress {
						break
					}
				}
			}
			for range 20 {
				progress, err := s.service.RunTurn(ctx)
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, contract.ErrMaintenanceDisabled) || errors.Is(err, contract.ErrMaintenancePaused) || errors.Is(err, contract.ErrBudgetDeferred) {
						return
					}
					s.logFailure("maintenance_turn_failed")
					return
				}
				if !progress {
					return
				}
			}
		}()
	}
	workers.Wait()
}

func (s *MaintenanceScheduler) logFailure(code string) {
	if s.logger != nil {
		s.logger.Error("ontology maintenance failed", slog.String("error_code", code))
	}
}
