package registry

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	SubmitRecallSessionFeedbackToolName  = "submit_recall_session_feedback"
	EvalListRecallFeedbackEventsToolName = "eval_list_recall_feedback_events"
	EvalGetRecallFeedbackEventToolName   = "eval_get_recall_feedback_event"
)

var evaluationToolNames = map[string]struct{}{
	"eval_get_manifest":                  {},
	"eval_list_knowledge_refs":           {},
	"eval_get_knowledge_item":            {},
	EvalListRecallFeedbackEventsToolName: {},
	EvalGetRecallFeedbackEventToolName:   {},
	"eval_run_dream_cycle":               {},
}

var recallFeedbackEventToolNames = map[string]struct{}{
	EvalListRecallFeedbackEventsToolName: {},
	EvalGetRecallFeedbackEventToolName:   {},
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

// EvaluationConfigProvider is the runtime config surface for evaluation tools.
type EvaluationConfigProvider interface {
	EvaluationRuntimeConfig(ctx context.Context) (domain.EvaluationRuntimeConfig, error)
}

// ToolVisible reports whether a registered tool should be visible for a request.
func ToolVisible(ctx context.Context, tool Tool, cfg RecallFeedbackConfigProvider) bool {
	if tool.Name == SubmitRecallSessionFeedbackToolName {
		return RecallFeedbackEnabled(ctx, cfg)
	}
	if _, ok := recallFeedbackEventToolNames[tool.Name]; ok {
		return RecallFeedbackEnabled(ctx, cfg)
	}
	if _, ok := evaluationToolNames[tool.Name]; ok {
		evalCfg, ok := cfg.(EvaluationConfigProvider)
		return ok && EvaluationEnabled(ctx, evalCfg)
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

// EvaluationEnabled fails closed because evaluation mode exposes broad
// team-scoped diagnostic data.
func EvaluationEnabled(ctx context.Context, cfg EvaluationConfigProvider) bool {
	if cfg == nil {
		return false
	}
	runtime, err := cfg.EvaluationRuntimeConfig(ctx)
	return err == nil && runtime.Enabled
}
