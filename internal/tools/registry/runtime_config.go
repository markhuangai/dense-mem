package registry

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const SubmitRecallSessionFeedbackToolName = "submit_recall_session_feedback"

// ErrToolDisabled is returned when a registered tool is unavailable under the
// current runtime feature configuration.
var ErrToolDisabled = errors.New("tool disabled")

// RecallFeedbackConfigProvider is the runtime config surface needed by
// recall-feedback request generation and execution.
type RecallFeedbackConfigProvider interface {
	RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error)
}

// ToolVisible reports whether a registered tool should be visible for a request.
// Tool schemas stay stable across long-lived clients; runtime config gates
// recall feedback requests and submissions instead of discovery.
func ToolVisible(_ context.Context, _ Tool, _ RecallFeedbackConfigProvider) bool {
	return true
}

// RecallFeedbackEnabled fails closed because this feature controls optional
// telemetry collection rather than core recall behavior.
func RecallFeedbackEnabled(ctx context.Context, cfg RecallFeedbackConfigProvider) bool {
	if cfg == nil {
		return false
	}
	runtime, err := cfg.RecallFeedbackRuntimeConfig(ctx)
	return err == nil && runtime.Enabled
}
