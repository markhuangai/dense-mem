package topics

import (
	"context"
	"errors"
	"log/slog"
	"time"

	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
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
		s.logFailure("configuration_unavailable", err)
		return
	}
	if !policy.Enabled {
		return
	}
	window, err := s.service.ensureWindow(ctx, policy, s.service.deps.Now())
	if err != nil {
		s.logFailure("window_unavailable", err)
		return
	}
	if window == nil {
		return
	}
	remaining := 20 * window.Policy.MaxConcurrency
	type turnResult struct {
		generation int
		progress   bool
		err        error
	}
	completed := make(chan turnResult, window.Policy.MaxConcurrency)
	active, generation := 0, 1
	blocked := 0
	finish := func() {
		result := <-completed
		active--
		s.logTurnFailure(result.err)
		if result.progress {
			generation++
		} else {
			// A late refusal must not hide capacity released after its dispatch.
			blocked = max(blocked, result.generation)
		}
	}
	for ctx.Err() == nil {
		if active == window.Policy.MaxConcurrency || remaining == 0 || blocked >= generation {
			if active == 0 {
				break
			}
			finish()
			continue
		}
		remaining--
		work, progress, err := s.service.prepareTurn(ctx)
		if err != nil {
			s.logTurnFailure(err)
			remaining = 0
		} else if progress {
			generation++
		} else {
			blocked = generation
		}
		if work != nil {
			active++
			go func() {
				completed <- turnResult{progress: true, err: s.service.executeBatch(ctx, *work)}
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
			s.logger.Error("topic maintenance finalization failed",
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
	code := maintenanceDiagnosticError(err)
	if code == "" || code == "maintenance_failure" {
		code = "maintenance_turn_failed"
	}
	s.logFailure(code, err)
}

func (s *MaintenanceScheduler) logFailure(code string, err error) {
	if s.logger == nil {
		return
	}
	attrs := []any{slog.String("error_code", code)}
	if err != nil {
		attrs = append(attrs, slog.String("failure_class", turnFailureClass(err)))
		if state := sqlStateOf(err); state != "" {
			attrs = append(attrs, slog.String("sqlstate", state))
		}
		var turnErr *maintenanceTurnError
		if errors.As(err, &turnErr) {
			if turnErr.RunID != "" {
				attrs = append(attrs, slog.String("run_id", turnErr.RunID))
			}
			if turnErr.BatchID != "" {
				attrs = append(attrs, slog.String("batch_id", turnErr.BatchID))
			}
			if turnErr.TeamID != "" {
				attrs = append(attrs, slog.String("team_id", turnErr.TeamID))
			}
			if turnErr.Stage != "" {
				attrs = append(attrs, slog.String("stage", turnErr.Stage))
			}
		}
		attrs = append(attrs, slog.String("error", boundedDiagnosticErrorText(err)))
	}
	s.logger.Error("topic maintenance failed", attrs...)
}
