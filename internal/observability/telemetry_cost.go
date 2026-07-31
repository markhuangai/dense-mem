package observability

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	AIOperationPlacementAssessment = "placement_assessment"
	AIOperationRecallEmbedding     = "recall_embedding"
	AIOperationBackgroundEmbedding = "background_embedding"

	AIComponentVerifier  = "verifier"
	AIComponentEmbedding = "embedding"

	AITokenSourceProvider  = "provider"
	AITokenSourceTokenizer = "tokenizer"
)

type telemetryMetricIdentityContextKey struct{}
type aiOperationContextKey struct{}

type telemetryMetricIdentity struct {
	teamID    string
	profileID string
}

type aiOperationContext struct {
	operation string
	itemCount int
}

// AIPricing is the rate card used only to estimate telemetry cost. Nil rates
// intentionally leave the matching token type unpriced.
type AIPricing struct {
	VerifierInputUSDPerMillionTokens  *float64
	VerifierOutputUSDPerMillionTokens *float64
	EmbeddingInputUSDPerMillionTokens *float64
}

// AIPricingResolver reads the current operator-managed rate card without
// coupling observability to a configuration implementation.
type AIPricingResolver interface {
	ResolveAIPricing(ctx context.Context) (AIPricing, error)
}

type AIPricingResolverFunc func(context.Context) (AIPricing, error)

func (f AIPricingResolverFunc) ResolveAIPricing(ctx context.Context) (AIPricing, error) {
	return f(ctx)
}

// AIOperationUsage is a bounded provider or tokenizer token observation.
type AIOperationUsage struct {
	Component    string
	Model        string
	InputTokens  int64
	OutputTokens int64
	ItemCount    int
	Source       string
}

// AIOperationMetrics is optional so existing domain-level metric fakes keep
// their narrow interface while production records cost telemetry.
type AIOperationMetrics interface {
	ObserveAIOperationUsage(ctx context.Context, usage AIOperationUsage)
	ObserveAIOperationUnpriced(ctx context.Context, component, model, reason string)
	ObserveRememberAcknowledgement(ctx context.Context, durationSeconds float64, outcome string)
	ObserveRememberFirstDisposition(ctx context.Context, durationSeconds float64, status string)
}

// WithMetricIdentity attaches a verified internal worker identity. Request
// contexts continue to use the authenticated actor identity automatically.
func WithMetricIdentity(ctx context.Context, teamID, profileID string) context.Context {
	return context.WithValue(ctx, telemetryMetricIdentityContextKey{}, telemetryMetricIdentity{
		teamID:    metricUUIDLabel(teamID),
		profileID: metricUUIDLabel(profileID),
	})
}

// WithAIOperation identifies a bounded AI operation whose cost should be
// recorded. Untagged provider calls retain provider telemetry but no cost data.
func WithAIOperation(ctx context.Context, operation string, itemCount int) context.Context {
	if itemCount < 1 {
		itemCount = 1
	}
	return context.WithValue(ctx, aiOperationContextKey{}, aiOperationContext{
		operation: normalizeAIOperation(operation),
		itemCount: itemCount,
	})
}

// HasAIOperation reports whether a bounded cost operation is attached to ctx.
func HasAIOperation(ctx context.Context) bool {
	_, ok := aiOperationFromContext(ctx)
	return ok
}

func metricIdentityFromContext(ctx context.Context) (telemetryMetricIdentity, bool) {
	identity, ok := ctx.Value(telemetryMetricIdentityContextKey{}).(telemetryMetricIdentity)
	return identity, ok
}

func aiOperationFromContext(ctx context.Context) (aiOperationContext, bool) {
	operation, ok := ctx.Value(aiOperationContextKey{}).(aiOperationContext)
	if !ok || operation.operation == unknownMetricLabel {
		return aiOperationContext{}, false
	}
	return operation, true
}

func metricUUIDLabel(value string) string {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil {
		return unknownMetricLabel
	}
	return parsed.String()
}

func normalizeAIOperation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AIOperationPlacementAssessment, AIOperationRecallEmbedding, AIOperationBackgroundEmbedding:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeAIComponent(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AIComponentVerifier, AIComponentEmbedding:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeAITokenSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AITokenSourceProvider, AITokenSourceTokenizer:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeAIUnpricedReason(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "missing_usage", "missing_price", "tokenizer_error", "invalid_usage", "pricing_unavailable":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeRememberOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ok", "error":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizePlacementStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed", "awaiting_review", "failed", "quarantined":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func RecordAIOperationUsage(ctx context.Context, metrics DiscoverabilityMetrics, usage AIOperationUsage) {
	if metrics == nil {
		return
	}
	if recorder, ok := metrics.(AIOperationMetrics); ok {
		recorder.ObserveAIOperationUsage(ctx, usage)
	}
}

func RecordAIOperationUnpriced(ctx context.Context, metrics DiscoverabilityMetrics, component, model, reason string) {
	if metrics == nil {
		return
	}
	if recorder, ok := metrics.(AIOperationMetrics); ok {
		recorder.ObserveAIOperationUnpriced(ctx, component, model, reason)
	}
}

func RecordRememberAcknowledgement(ctx context.Context, metrics DiscoverabilityMetrics, duration time.Duration, outcome string) {
	if metrics == nil || duration < 0 {
		return
	}
	if recorder, ok := metrics.(AIOperationMetrics); ok {
		recorder.ObserveRememberAcknowledgement(ctx, duration.Seconds(), outcome)
	}
}

func RecordRememberFirstDisposition(ctx context.Context, metrics DiscoverabilityMetrics, duration time.Duration, status string) {
	if metrics == nil || duration < 0 {
		return
	}
	if recorder, ok := metrics.(AIOperationMetrics); ok {
		recorder.ObserveRememberFirstDisposition(ctx, duration.Seconds(), status)
	}
}
