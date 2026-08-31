package registry

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

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

// ContractToolRuntimeOptional reports whether a contract tool may be omitted
// from discovery while its runtime feature is disabled.
func ContractToolRuntimeOptional(name string) bool {
	if name == ToolSubmitRecallSessionFeedback {
		return true
	}
	_, ok := dreamToolNames[name]
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
// policy. The argument is a fallback team ID for non-request callers;
// authenticated request context takes precedence.
type DreamingConfigProvider interface {
	EffectiveConfig(ctx context.Context, fallbackTeamID string) (dreamservice.EffectiveConfig, error)
}

// RuntimeToolPolicy contains request-time feature dependencies shared by MCP
// discovery and invocation.
type RuntimeToolPolicy struct {
	RecallFeedback RecallFeedbackConfigProvider
	Dreams         DreamingConfigProvider
	resolved       *runtimeToolFeatures
}

type runtimeToolFeatures struct {
	recallFeedbackEnabled  bool
	recallFeedbackResolved bool
	dreamingEnabled        bool
	dreamingResolved       bool
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
	needsDreaming := false
	for _, tool := range tools {
		if tool.Name == ToolSubmitRecallSessionFeedback || tool.Name == ToolRecallMemory {
			needsRecallFeedback = true
		}
		if tool.Name == ToolRecallMemory {
			needsDreaming = true
		}
		if _, ok := dreamToolNames[tool.Name]; ok {
			needsDreaming = true
		}
	}
	if needsRecallFeedback && !features.recallFeedbackResolved {
		features.recallFeedbackEnabled = recallFeedbackEnabledFromConfig(ctx, policy.RecallFeedback)
		features.recallFeedbackResolved = true
	}
	if needsDreaming && !features.dreamingResolved {
		features.dreamingEnabled = dreamingEnabledFromConfig(ctx, policy.Dreams)
		features.dreamingResolved = true
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
	policy = ResolveRuntimeToolPolicy(ctx, policy, tool)
	if tool.Name == ToolSubmitRecallSessionFeedback {
		return policy.resolved.recallFeedbackEnabled
	}
	if _, ok := dreamToolNames[tool.Name]; ok {
		return policy.resolved.dreamingEnabled
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

// DreamingEnabled fails closed so disabled or unreadable team configuration
// cannot expose Hypothesis lifecycle tools.
func DreamingEnabled(ctx context.Context, cfg DreamingConfigProvider) bool {
	if resolved, ok := ctx.Value(runtimeToolFeaturesContextKey{}).(runtimeToolFeatures); ok && resolved.dreamingResolved {
		return resolved.dreamingEnabled
	}
	return dreamingEnabledFromConfig(ctx, cfg)
}

func recallFeedbackEnabledFromConfig(ctx context.Context, cfg RecallFeedbackConfigProvider) bool {
	if cfg == nil {
		return false
	}
	runtime, err := cfg.RecallFeedbackRuntimeConfig(ctx)
	return err == nil && runtime.Enabled
}

func dreamingEnabledFromConfig(ctx context.Context, cfg DreamingConfigProvider) bool {
	if cfg == nil {
		return false
	}
	effective, err := cfg.EffectiveConfig(ctx, "")
	return err == nil && effective.Enabled
}
