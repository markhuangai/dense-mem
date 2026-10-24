package ontology

import (
	"context"
	"errors"
	"log/slog"
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
	window, err := s.service.ensureWindow(ctx, policy, s.service.deps.Now())
	if err != nil {
		s.logFailure("window_unavailable")
		return
	}
	if window == nil {
		return
	}
	const organizationTurn, projectionTurn = 0, 1
	remaining := [2]int{20 * window.Policy.MaxConcurrency, 20 * window.Policy.MaxConcurrency}
	if s.service.deps.Projections == nil {
		remaining[projectionTurn] = 0
	} else {
		status, err := s.service.deps.Repository.MaintenanceStatus(ctx, s.service.deps.Now())
		if err != nil {
			s.logFailure("projection_status_unavailable")
		}
		if err != nil || status.Paused {
			remaining[projectionTurn] = 0
		}
	}
	type turnResult struct {
		kind, generation int
		progress         bool
		err              error
	}
	completed := make(chan turnResult, window.Policy.MaxConcurrency)
	active, next, generation := 0, organizationTurn, 1
	blocked := [2]int{}
	finish := func() {
		result := <-completed
		active--
		if result.kind == organizationTurn {
			s.logTurnFailure(result.err)
		} else if result.err != nil {
			s.logFailure("topic_projection_failed")
			remaining[projectionTurn] = 0
		}
		if result.progress {
			generation++
		} else {
			// A late refusal must not hide capacity released after its dispatch.
			blocked[result.kind] = max(blocked[result.kind], result.generation)
		}
	}
	for ctx.Err() == nil {
		kind := next
		if remaining[kind] == 0 || blocked[kind] >= generation {
			kind = 1 - kind
		}
		if active == window.Policy.MaxConcurrency || remaining[kind] == 0 || blocked[kind] >= generation {
			if active == 0 {
				break
			}
			finish()
			continue
		}
		remaining[kind]--
		next = 1 - kind
		if kind == projectionTurn {
			active++
			dispatched := generation
			go func() {
				progress, err := s.service.deps.Projections.RunProjectionTurn(ctx)
				completed <- turnResult{kind: projectionTurn, generation: dispatched, progress: progress, err: err}
			}()
			continue
		}
		work, progress, err := s.service.prepareTurn(ctx)
		if err != nil {
			s.logTurnFailure(err)
			remaining[organizationTurn] = 0
		} else if progress {
			generation++
		} else {
			blocked[organizationTurn] = generation
		}
		if work != nil {
			active++
			go func() {
				completed <- turnResult{kind: organizationTurn, progress: true, err: s.service.executeBatch(ctx, *work)}
			}()
		}
	}
	for active > 0 {
		finish()
	}
}

func (s *MaintenanceScheduler) logTurnFailure(err error) {
	var finalization *contract.MaintenanceFinalizationError
	if errors.As(err, &finalization) {
		if s.logger != nil {
			s.logger.Error("ontology maintenance finalization failed",
				slog.String("error_code", "maintenance_finalization_failed"),
				slog.String("batch_id", finalization.BatchID), slog.String("run_id", finalization.RunID),
				slog.String("phase", finalization.Phase), slog.String("failure_class", finalization.FailureClass()),
				slog.String("sqlstate", finalization.SQLState()), slog.Int64("duration_ms", finalization.Duration.Milliseconds()))
		}
		return
	}
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, contract.ErrMaintenanceDisabled) || errors.Is(err, contract.ErrMaintenancePaused) || errors.Is(err, contract.ErrWindowClosed) {
		return
	}
	s.logFailure("maintenance_turn_failed")
}

func (s *MaintenanceScheduler) logFailure(code string) {
	if s.logger != nil {
		s.logger.Error("ontology maintenance failed", slog.String("error_code", code))
	}
}
