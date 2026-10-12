package registry

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var evaluationToolNames = map[string]struct{}{
	"eval_list_knowledge_refs": {},
	"eval_run_recall_case":     {},
}

// IsEvaluationTool reports whether a tool belongs to the evaluation-only surface.
func IsEvaluationTool(name string) bool {
	_, ok := evaluationToolNames[name]
	return ok
}

// ContractToolRuntimeOptional reports whether a contract tool may be omitted
// from discovery while its runtime feature is disabled.
func ContractToolRuntimeOptional(name string) bool {
	if name == ToolIngestSession {
		return true
	}
	if name == ToolSubmitRecallSessionFeedback {
		return true
	}
	return false
}

// ErrToolDisabled is returned when a registered tool is unavailable under the
// current runtime feature configuration.
var ErrToolDisabled = errors.New("tool disabled")

// RecallFeedbackConfigProvider is the runtime config surface needed by
// recall-feedback request generation and execution.
type RecallFeedbackConfigProvider interface {
	RecallFeedbackRuntimeConfig(ctx context.Context) (domain.RecallFeedbackRuntimeConfig, error)
}

// RuntimeToolPolicy contains request-time feature dependencies shared by MCP
// discovery and invocation.
type RuntimeToolPolicy struct {
	RecallFeedback RecallFeedbackConfigProvider

	resolved *runtimeToolFeatures
}

type runtimeToolFeatures struct {
	recallFeedbackEnabled  bool
	recallFeedbackResolved bool
}

type runtimeToolFeaturesContextKey struct{}

// ResolveRuntimeToolPolicy evaluates only the feature config needed by the
// supplied tools and reuses any values already resolved in this request.
func ResolveRuntimeToolPolicy(ctx context.Context, policy RuntimeToolPolicy, tools ...Tool) RuntimeToolPolicy {
	features := runtimeToolFeatures{}
	if policy.resolved != nil {
		features = *policy.resolved
	}
	needsRecallFeedback := false
	for _, tool := range tools {
		if tool.Name == ToolSubmitRecallSessionFeedback || tool.Name == ToolRecallMemory {
			needsRecallFeedback = true
		}
	}
	if needsRecallFeedback && !features.recallFeedbackResolved {
		features.recallFeedbackEnabled = recallFeedbackEnabledFromConfig(ctx, policy.RecallFeedback)
		features.recallFeedbackResolved = true
	}
	policy.resolved = &features
	return policy
}

// WithRuntimeToolPolicy makes a resolved feature snapshot available to tool
// invokers in the same request.
func WithRuntimeToolPolicy(ctx context.Context, policy RuntimeToolPolicy) context.Context {
	if policy.resolved == nil {
		return ctx
	}
	return context.WithValue(ctx, runtimeToolFeaturesContextKey{}, *policy.resolved)
}

// ToolVisible reports whether a registered tool should be visible for a request.
func ToolVisible(ctx context.Context, tool Tool, policy RuntimeToolPolicy) bool {
	if tool.Name == ToolIngestSession {
		return tool.Available != nil && tool.Available(ctx)
	}
	policy = ResolveRuntimeToolPolicy(ctx, policy, tool)
	if tool.Name == ToolSubmitRecallSessionFeedback {
		return policy.resolved.recallFeedbackEnabled
	}
	if IsContractTool(tool) {
		return true
	}
	if tool.FeatureGate == domain.FeatureGate && tool.Visibility == domain.ToolVisibility {
		return false
	}
	return true
}

// RecallFeedbackEnabled fails closed because this feature controls optional
// telemetry collection rather than core recall behavior.
func RecallFeedbackEnabled(ctx context.Context, cfg RecallFeedbackConfigProvider) bool {
	if resolved, ok := ctx.Value(runtimeToolFeaturesContextKey{}).(runtimeToolFeatures); ok && resolved.recallFeedbackResolved {
		return resolved.recallFeedbackEnabled
	}
	return recallFeedbackEnabledFromConfig(ctx, cfg)
}

func recallFeedbackEnabledFromConfig(ctx context.Context, cfg RecallFeedbackConfigProvider) bool {
	if cfg == nil {
		return false
	}
	runtime, err := cfg.RecallFeedbackRuntimeConfig(ctx)
	return err == nil && runtime.Enabled
}
