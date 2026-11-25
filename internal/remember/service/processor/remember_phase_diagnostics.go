package processor

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func (p *rememberSynchronousProcessor) observeRememberPhase(ctx context.Context, input rememberapp.RememberProcessRequest, attemptID, phase string, err error, started time.Time, diagnostics map[string]any) {
	duration := time.Since(started)
	outcome := rememberMetricPhaseOutcome(err)
	metricPhase := phase
	if phase == "duplicate_embedding" {
		metricPhase = "embedding"
	}
	observability.RecordRememberPhase(p.metrics, metricPhase, outcome, duration)
	if p.logger == nil {
		return
	}
	attrs := []observability.LogAttr{
		observability.String("team_id", input.TeamID),
		observability.String("owner_profile_id", input.OwnerProfileID),
		observability.String("attempt_id", attemptID),
		observability.String("correlation_id", rememberProcessCorrelationID(input.Metadata)),
		observability.String("phase", phase),
		observability.String("outcome", outcome),
		{Key: "duration_ms", Value: duration.Milliseconds()},
	}
	if diagnostics != nil {
		attrs = append(attrs, observability.LogAttr{Key: "assessor_diagnostics", Value: diagnostics})
	}
	if contextual, ok := p.logger.(observability.ContextLogProvider); ok {
		contextual.InfoContext(ctx, "remember_phase_completed", attrs...)
	} else {
		p.logger.Info("remember_phase_completed", attrs...)
	}
}
