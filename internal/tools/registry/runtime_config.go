package registry

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

const SubmitRecallSessionFeedbackToolName = "submit_recall_session_feedback"

var evaluationToolNames = map[string]struct{}{
	"eval_list_knowledge_refs": {},
	"eval_run_dream_cycle":     {},
	"eval_run_recall_case":     {},
}

var dreamToolNames = map[string]struct{}{
	ToolListDreams:           {},
	ToolGetDream:             {},
	ToolResolveDreamFeedback: {},
}

// IsEvaluationTool reports whether a tool belongs to the evaluation-only surface.
func IsEvaluationTool(name string) bool {
	_, ok := evaluationToolNames[name]
	return ok
}

// ErrToolDisabled is returned when a registered tool is unavailable under the
// current runtime feature configuration.
var ErrToolDisabled = errors.New("tool disabled")

// RecallFeedbackConfigProvider is the runtime config surface needed by
// recall-feedback request generation and execution.
type RecallFeedbackConfigProvider interface {
	RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error)
}

// DreamingConfigProvider resolves the authenticated team's effective Dreaming
// policy. Implementations must derive team identity from the request context.
type DreamingConfigProvider interface {
	EffectiveConfig(ctx context.Context, profileID string) (dreamservice.EffectiveConfig, error)
}

// RuntimeToolPolicy contains request-time feature dependencies shared by MCP
// discovery and invocation.
type RuntimeToolPolicy struct {
	RecallFeedback RecallFeedbackConfigProvider
	Dreams         DreamingConfigProvider
	ProfileID      string
}

// ToolVisible reports whether a registered tool should be visible for a request.
func ToolVisible(ctx context.Context, tool Tool, policy RuntimeToolPolicy) bool {
	if tool.FeatureGate == domain.FeatureGate && tool.Visibility == domain.ToolVisibility {
		return false
	}
	if tool.Name == SubmitRecallSessionFeedbackToolName {
		return RecallFeedbackEnabled(ctx, policy.RecallFeedback)
	}
	if _, ok := dreamToolNames[tool.Name]; ok {
		return DreamingEnabled(ctx, policy.Dreams, policy.ProfileID)
	}
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

// DreamingEnabled fails closed so disabled or unreadable team configuration
// cannot expose Hypothesis lifecycle tools.
func DreamingEnabled(ctx context.Context, cfg DreamingConfigProvider, profileID string) bool {
	if cfg == nil {
		return false
	}
	effective, err := cfg.EffectiveConfig(ctx, profileID)
	return err == nil && effective.Enabled
}
