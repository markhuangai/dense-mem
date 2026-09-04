package observability

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAIOperationContextUsesBoundedLabels(t *testing.T) {
	ctx := context.Background()
	if HasAIOperation(ctx) {
		t.Fatal("untagged context unexpectedly has an AI operation")
	}

	operationCtx := WithAIOperation(ctx, " RECALL_EMBEDDING ", 0)
	operation, ok := aiOperationFromContext(operationCtx)
	if !ok {
		t.Fatal("tagged context does not have an AI operation")
	}
	if operation.operation != AIOperationRecallEmbedding || operation.itemCount != 1 {
		t.Fatalf("operation = %#v; want normalized recall embedding with one item", operation)
	}
	if !HasAIOperation(operationCtx) {
		t.Fatal("tagged context does not report an AI operation")
	}
	if HasAIOperation(WithAIOperation(ctx, "unbounded-operation", 2)) {
		t.Fatal("unknown operation unexpectedly has an AI operation")
	}
	graphCtx := WithAIOperation(ctx, AIOperationDreamGeneration, 1)
	evidenceCtx := WithAIOperation(ctx, AIOperationEvidenceDiscovery, 1)
	graphOperation, graphOK := aiOperationFromContext(graphCtx)
	evidenceOperation, evidenceOK := aiOperationFromContext(evidenceCtx)
	if !graphOK || !evidenceOK || graphOperation.operation == evidenceOperation.operation {
		t.Fatalf("graph/evidence AI operations are not distinct: graph=%#v evidence=%#v", graphOperation, evidenceOperation)
	}

	profileID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	identityCtx := WithMetricIdentity(ctx, "not-a-uuid", profileID.String())
	identity, ok := metricIdentityFromContext(identityCtx)
	if !ok {
		t.Fatal("metric identity is missing")
	}
	if identity.teamID != unknownMetricLabel || identity.profileID != profileID.String() {
		t.Fatalf("metric identity = %#v; want unknown team and valid profile", identity)
	}

	for _, test := range []struct {
		name string
		got  string
	}{
		{name: "operation", got: normalizeAIOperation("unbounded-operation")},
		{name: "component", got: normalizeAIComponent("unbounded-component")},
		{name: "source", got: normalizeAITokenSource("unbounded-source")},
		{name: "reason", got: normalizeAIUnpricedReason("unbounded-reason")},
		{name: "remember outcome", got: normalizeRememberOutcome("unbounded-outcome")},
	} {
		if test.got != unknownMetricLabel {
			t.Errorf("%s label = %q; want %q", test.name, test.got, unknownMetricLabel)
		}
	}
}

func TestAIOperationCostUSDRequiresCompleteRateCard(t *testing.T) {
	verifierInput := 2.0
	verifierOutput := 4.0
	embeddingInput := 1.5
	tests := []struct {
		name      string
		component string
		usage     AIOperationUsage
		pricing   AIPricing
		want      float64
		priced    bool
	}{
		{
			name:      "verifier input rate missing",
			component: AIComponentVerifier,
			usage:     AIOperationUsage{InputTokens: 10},
			pricing:   AIPricing{VerifierOutputUSDPerMillionTokens: &verifierOutput},
		},
		{
			name:      "verifier output rate missing",
			component: AIComponentVerifier,
			usage:     AIOperationUsage{OutputTokens: 10},
			pricing:   AIPricing{VerifierInputUSDPerMillionTokens: &verifierInput},
		},
		{
			name:      "verifier is priced",
			component: AIComponentVerifier,
			usage:     AIOperationUsage{InputTokens: 1_000_000, OutputTokens: 500_000},
			pricing: AIPricing{
				VerifierInputUSDPerMillionTokens:  &verifierInput,
				VerifierOutputUSDPerMillionTokens: &verifierOutput,
			},
			want:   4,
			priced: true,
		},
		{
			name:      "embedding rate missing",
			component: AIComponentEmbedding,
			usage:     AIOperationUsage{InputTokens: 10},
			pricing:   AIPricing{},
		},
		{
			name:      "embedding is priced",
			component: AIComponentEmbedding,
			usage:     AIOperationUsage{InputTokens: 2_000_000},
			pricing:   AIPricing{EmbeddingInputUSDPerMillionTokens: &embeddingInput},
			want:      3,
			priced:    true,
		},
		{
			name:      "unknown component",
			component: "unbounded-component",
			usage:     AIOperationUsage{InputTokens: 10},
			pricing:   AIPricing{EmbeddingInputUSDPerMillionTokens: &embeddingInput},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, priced := aiOperationCostUSD(test.component, test.usage, test.pricing)
			if priced != test.priced {
				t.Fatalf("priced = %t; want %t", priced, test.priced)
			}
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("cost = %v; want %v", got, test.want)
			}
		})
	}
}

func TestPrometheusMetricsRecordsCostBoundaryOutcomes(t *testing.T) {
	pricingUnavailable := errors.New("rate card unavailable")
	metrics := NewPrometheusMetrics(AIPricingResolverFunc(func(context.Context) (AIPricing, error) {
		return AIPricing{}, pricingUnavailable
	}))
	teamID := uuid.MustParse("55555555-5555-4555-8555-555555555555")
	profileID := uuid.MustParse("66666666-6666-4666-8666-666666666666")
	ctx := WithMetricIdentity(context.Background(), teamID.String(), profileID.String())
	operationCtx := WithAIOperation(ctx, AIOperationRecallEmbedding, 1)

	metrics.ObserveRecallFor(ctx, 1, -1, "ok")
	metrics.ObserveRememberAcknowledgement(ctx, -1, "ok")
	metrics.ObserveAIOperationUsage(operationCtx, AIOperationUsage{
		Component: AIComponentEmbedding,
		Model:     "configured-embedding",
		Source:    AITokenSourceProvider,
	})
	metrics.ObserveAIOperationUsage(operationCtx, AIOperationUsage{
		Component:   AIComponentEmbedding,
		Model:       "configured-embedding",
		InputTokens: 1,
		Source:      AITokenSourceProvider,
	})
	metrics.ObserveAIOperationUnpriced(context.Background(), AIComponentEmbedding, "untagged", "missing_price")
	metrics.ObserveAIOperationUnpriced(operationCtx, "unbounded-component", "configured-embedding", "unbounded-reason")

	missingPriceMetrics := NewPrometheusMetrics(AIPricingResolverFunc(func(context.Context) (AIPricing, error) {
		return AIPricing{}, nil
	}))
	missingPriceMetrics.ObserveAIOperationUsage(operationCtx, AIOperationUsage{
		Component:   AIComponentEmbedding,
		Model:       "configured-embedding",
		InputTokens: 1,
		Source:      AITokenSourceProvider,
	})

	identity := []string{teamID.String(), profileID.String()}
	body := scrapePrometheusMetrics(t, metrics)
	if strings.Contains(body, `model="untagged"`) {
		t.Fatal("untagged operation unexpectedly emitted a metric")
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_unpriced_total", append(identity, "operation=\"recall_embedding\"", "component=\"embedding\"", "model=\"configured-embedding\"", "reason=\"missing_usage\"")...); got != 1 {
		t.Fatalf("missing-usage count = %v; want 1", got)
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_unpriced_total", append(identity, "operation=\"recall_embedding\"", "component=\"embedding\"", "model=\"configured-embedding\"", "reason=\"pricing_unavailable\"")...); got != 1 {
		t.Fatalf("pricing-unavailable count = %v; want 1", got)
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_unpriced_total", append(identity, "operation=\"recall_embedding\"", "component=\"unknown\"", "model=\"configured-embedding\"", "reason=\"unknown\"")...); got != 1 {
		t.Fatalf("unknown bounded-label count = %v; want 1", got)
	}
	if got := prometheusCounterValue(t, scrapePrometheusMetrics(t, missingPriceMetrics), "densemem_ai_operation_unpriced_total", append(identity, "operation=\"recall_embedding\"", "component=\"embedding\"", "model=\"configured-embedding\"", "reason=\"missing_price\"")...); got != 1 {
		t.Fatalf("missing-price count = %v; want 1", got)
	}
}
