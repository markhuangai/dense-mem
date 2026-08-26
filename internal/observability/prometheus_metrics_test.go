package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestPrometheusMetricsRecordsActiveScopedSignals(t *testing.T) {
	metrics := NewPrometheusMetrics()
	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID:    teamID,
		TeamName:  "Research",
		OwnerID:   profileID,
		OwnerName: "Profile A",
	})

	metrics.ObserveHTTPRequest(ctx, "/ui/api/evidence/:id", "get", http.StatusTeapot, 15*time.Millisecond)
	metrics.ObserveEmbeddingLatencyFor(ctx, "embed-model", 123, "ok")
	metrics.IncEmbeddingErrorFor(ctx, "embed-model", "timeout")
	metrics.ObserveEmbeddingTokens(ctx, "embed-model", 11, 22)
	metrics.ObserveVerifierLatencyFor(ctx, "verify-model", 321, "ok")
	metrics.ObserveVerifierTokens(ctx, "verify-model", 7, 8, 15)
	metrics.IncVerifyVerdictFor(ctx, "verify-model", "verified")
	metrics.ObserveRecallLatencyFor(ctx, 42)
	metrics.ObserveRecallFor(ctx, 42, 3, "ok")
	metrics.ObserveRecallFeedbackFor(ctx, RecallFeedback{Used: true, AnswerSupported: true, Quality: "high"})
	metrics.ObserveDreamFeedbackFor(ctx, DreamFeedback{Decision: "confirm_true", Outcome: "ok", FromStatus: "proposed"})
	metrics.ObserveConflictReviewDurationFor(ctx, 2.5, "completed")
	metrics.IncSubmissionQuarantinePurgeFailure()

	body := scrapePrometheusMetrics(t, metrics)
	for _, want := range []string{
		`densemem_http_requests_total{`,
		`team_id="11111111-1111-4111-8111-111111111111"`,
		`profile_id="22222222-2222-4222-8222-222222222222"`,
		`route="/ui/api/evidence/:id"`,
		`method="GET"`,
		`status_class="4xx"`,
		`densemem_embedding_tokens_total{`,
		`model="embed-model"`,
		`kind="prompt"`,
		`kind="total"`,
		`densemem_verifier_tokens_total{`,
		`kind="completion"`,
		`densemem_verify_verdict_total{`,
		`outcome="verified"`,
		`densemem_recall_results_bucket{`,
		`densemem_recall_feedback_total{`,
		`used="true"`,
		`answer_supported="true"`,
		`quality="high"`,
		`densemem_dream_feedback_total{`,
		`decision="confirm_true"`,
		`densemem_conflict_review_duration_seconds_bucket{`,
		`outcome="completed"`,
		`densemem_submission_quarantine_purge_failures_total 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scraped metrics missing %q\n%s", want, body)
		}
	}
	for _, retired := range []string{
		"densemem_memory_funnel_latency_seconds",
		"densemem_fragment_create_total",
		"densemem_claim_create_total",
		"densemem_promotion_outcome_total",
		"densemem_promote_lock_wait_seconds",
		"densemem_retractions_total",
		"densemem_fact_needs_revalidation_total",
		"densemem_community_detect_total",
	} {
		if strings.Contains(body, retired) {
			t.Fatalf("scraped metrics still contain retired metric %q\n%s", retired, body)
		}
	}
	for _, blocked := range []string{"team_name=", "profile_name=", "Research", "Profile A"} {
		if strings.Contains(body, blocked) {
			t.Fatalf("scraped metrics leaked %q\n%s", blocked, body)
		}
	}
}

func TestPrometheusMetricsRecordsUnknownLabelsAndHelpers(t *testing.T) {
	metrics := NewPrometheusMetrics()
	ctx := context.Background()

	metrics.ObserveHTTPRequest(ctx, "", "", 99, time.Millisecond)
	metrics.ObserveEmbeddingLatency(1, "")
	metrics.IncEmbeddingError("")
	metrics.ObserveRecallLatency(2)
	metrics.ObserveRecall(2, 0, "")
	metrics.ObserveRecallFeedback(RecallFeedback{Quality: "bad"})
	metrics.ObserveDreamFeedback(DreamFeedback{})
	metrics.ObserveConflictReviewDuration(1, "")
	metrics.ObserveConflictReviewDurationFor(ctx, -1, "ignored")
	metrics.IncVerifyVerdict("")

	RecordEmbeddingLatency(ctx, metrics, "embed-model", 1, "ok")
	RecordEmbeddingError(ctx, metrics, "embed-model", "error")
	RecordEmbeddingTokens(ctx, metrics, "embed-model", 1, 2)
	RecordVerifierLatency(ctx, metrics, "verify-model", 1, "ok")
	RecordVerifierTokens(ctx, metrics, "verify-model", 1, 1, 2)
	RecordVerifyVerdict(ctx, metrics, "verify-model", "verified")
	RecordRecallLatency(ctx, metrics, 1)
	RecordRecall(ctx, metrics, 1, 2, "ok")
	RecordRecallFeedback(ctx, metrics, RecallFeedback{Quality: "medium"})
	RecordDreamFeedback(ctx, metrics, DreamFeedback{Decision: "reject", Outcome: "error", FromStatus: "proposed"})
	RecordConflictReviewDuration(ctx, metrics, 1, "completed")
	exerciseNilMetricHelpers(ctx)

	body := scrapePrometheusMetrics(t, metrics)
	for _, want := range []string{
		`team_id="unknown"`,
		`profile_id="unknown"`,
		`route="unknown"`,
		`method="UNKNOWN"`,
		`status_class="5xx"`,
		`model="unknown"`,
		`outcome="unknown"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scraped metrics missing %q\n%s", want, body)
		}
	}
	requirePrometheusMetricLabels(t, body, "densemem_dream_feedback_total", `decision="unknown"`, `outcome="unknown"`, `from_status="unknown"`)
}

func TestPrometheusMetricsRecordsLifecycleAndPricedAIOperations(t *testing.T) {
	verifierInputPrice := 2.0
	verifierOutputPrice := 4.0
	embeddingInputPrice := 1.5
	metrics := NewPrometheusMetrics(AIPricingResolverFunc(func(context.Context) (AIPricing, error) {
		return AIPricing{
			VerifierInputUSDPerMillionTokens:  &verifierInputPrice,
			VerifierOutputUSDPerMillionTokens: &verifierOutputPrice,
			EmbeddingInputUSDPerMillionTokens: &embeddingInputPrice,
		}, nil
	}))
	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID})

	RecordRememberAcknowledgement(ctx, metrics, 120*time.Millisecond, "ok")
	RecordAIOperationUsage(
		WithAIOperation(ctx, AIOperationSemanticAssessment, 1),
		metrics,
		AIOperationUsage{
			Component:    AIComponentVerifier,
			Model:        "configured-verifier",
			InputTokens:  1_000_000,
			OutputTokens: 500_000,
			ItemCount:    2,
			Source:       AITokenSourceProvider,
		},
	)
	RecordAIOperationUsage(
		WithAIOperation(ctx, AIOperationRecallEmbedding, 1),
		metrics,
		AIOperationUsage{
			Component:   AIComponentEmbedding,
			Model:       "configured-embedding",
			InputTokens: 2_000_000,
			Source:      AITokenSourceTokenizer,
		},
	)

	identity := []string{teamID.String(), profileID.String()}
	body := scrapePrometheusMetrics(t, metrics)
	if got := prometheusCounterValue(t, body, "densemem_remember_acknowledgements_total", append(identity, "outcome=\"ok\"")...); got != 1 {
		t.Fatalf("remember acknowledgements = %v; want 1", got)
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_cost_usd_total", append(identity, "operation=\"semantic_assessment\"", "component=\"verifier\"", "model=\"configured-verifier\"", "source=\"provider\"")...); got != 4 {
		t.Fatalf("verifier cost = %v; want 4", got)
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_cost_usd_total", append(identity, "operation=\"recall_embedding\"", "component=\"embedding\"", "model=\"configured-embedding\"", "source=\"tokenizer\"")...); got != 3 {
		t.Fatalf("embedding cost = %v; want 3", got)
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_items_total", append(identity, "operation=\"semantic_assessment\"", "component=\"verifier\"", "model=\"configured-verifier\"", "source=\"provider\"")...); got != 2 {
		t.Fatalf("verifier item count = %v; want 2", got)
	}

	requirePrometheusMetricLabels(t, body, "densemem_remember_acknowledgement_duration_seconds_bucket", `outcome="ok"`)
}

func TestPrometheusMetricsMarksUnpricedAndKeepsWorkerIdentity(t *testing.T) {
	metrics := NewPrometheusMetrics()
	teamID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	profileID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	ctx := WithMetricIdentity(context.Background(), teamID.String(), profileID.String())
	operationCtx := WithAIOperation(ctx, AIOperationSemanticAssessment, 3)

	RecordAIOperationUsage(operationCtx, metrics, AIOperationUsage{
		Component:   AIComponentEmbedding,
		Model:       "configured-embedding",
		InputTokens: 12,
		Source:      AITokenSourceProvider,
	})
	RecordAIOperationUsage(operationCtx, metrics, AIOperationUsage{
		Component:    AIComponentEmbedding,
		Model:        "configured-embedding",
		InputTokens:  12,
		OutputTokens: 1,
		Source:       AITokenSourceProvider,
	})
	RecordAIOperationUsage(context.Background(), metrics, AIOperationUsage{
		Component:   AIComponentEmbedding,
		Model:       "untagged",
		InputTokens: 12,
		Source:      AITokenSourceProvider,
	})

	identity := []string{teamID.String(), profileID.String()}
	body := scrapePrometheusMetrics(t, metrics)
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_unpriced_total", append(identity, "operation=\"semantic_assessment\"", "component=\"embedding\"", "model=\"configured-embedding\"", "reason=\"pricing_unavailable\"")...); got != 1 {
		t.Fatalf("pricing-unavailable count = %v; want 1", got)
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_unpriced_total", append(identity, "operation=\"semantic_assessment\"", "component=\"embedding\"", "model=\"configured-embedding\"", "reason=\"invalid_usage\"")...); got != 1 {
		t.Fatalf("invalid-usage count = %v; want 1", got)
	}
	if got := prometheusCounterValue(t, body, "densemem_ai_operation_items_total", append(identity, "operation=\"semantic_assessment\"", "component=\"embedding\"", "model=\"configured-embedding\"", "source=\"provider\"")...); got != 3 {
		t.Fatalf("background item count = %v; want 3", got)
	}
	if strings.Contains(body, "untagged") {
		t.Fatal("untagged AI operation should not create a metric")
	}
}

func TestPrometheusMetrics_RecordsAssessorMetricsWithoutIdentityLabels(t *testing.T) {
	metrics := NewPrometheusMetrics()
	metrics.ObserveAssessorCall(200, 50, 0.25, "ok")
	metrics.IncAssessorValidationFailure("response")
	metrics.IncAssessorValidationFieldFailure("response_contract", "relationship_results.predicate")
	metrics.IncAssessorValidationFieldFailure("not-a-stage", "provider supplied arbitrary field")
	metrics.IncAssessorCandidateTruncation()
	metrics.IncAssessorAssessmentPersistence("persisted")
	metrics.IncAssessorDuplicateRequestPrevention("post_persist")
	metrics.IncAssessorConfidenceGate("below_write_threshold")
	metrics.IncAssessorTerminalFailure("predicate_options_overflow")

	body := scrapePrometheusMetrics(t, metrics)
	for _, want := range []string{
		`densemem_assessor_requests_total{outcome="ok"}`,
		`densemem_assessor_duration_seconds_bucket{outcome="ok",le=`,
		`densemem_assessor_duration_seconds_bucket{outcome="ok",le="600"}`,
		`densemem_assessor_tokens_total{kind="input"}`,
		`densemem_assessor_tokens_total{kind="output"}`,
		`densemem_assessor_validation_failures_total{stage="response"}`,
		`densemem_assessor_validation_field_failures_total{family="relationship_results.predicate",stage="response_contract"}`,
		`densemem_assessor_validation_field_failures_total{family="other",stage="unknown"}`,
		`densemem_assessor_candidate_truncations_total 1`,
		`densemem_assessor_assessment_persistence_total{outcome="persisted"}`,
		`densemem_assessor_duplicate_request_prevented_total{stage="post_persist"}`,
		`densemem_assessor_confidence_gate_total{band="below_write_threshold"}`,
		`densemem_assessor_terminal_failures_total{stage="predicate_options_overflow"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scraped assessor metrics missing %q\n%s", want, body)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "densemem_assessor_") && (strings.Contains(line, "team_id=") || strings.Contains(line, "profile_id=")) {
			t.Fatalf("assessor metric leaked identity label %q", line)
		}
	}
	if strings.Contains(body, "provider supplied arbitrary field") {
		t.Fatalf("assessor metric leaked an unbounded field label\n%s", body)
	}
}

func TestPrometheusMetricLabelHelpers(t *testing.T) {
	if got := identityLabels(); len(got) != 2 {
		t.Fatalf("identityLabels len = %d; want 2", len(got))
	}
	if got := uuidLabel(uuid.Nil); got != unknownMetricLabel {
		t.Fatalf("uuidLabel(nil) = %q; want %q", got, unknownMetricLabel)
	}
	if got := normalizeLabel("  value  "); got != "value" {
		t.Fatalf("normalizeLabel trimmed = %q; want value", got)
	}
	if got := normalizeLabel(" "); got != unknownMetricLabel {
		t.Fatalf("normalizeLabel blank = %q; want %q", got, unknownMetricLabel)
	}
	if got := recallFeedbackQualityScore("low"); got != 0 {
		t.Fatalf("recallFeedbackQualityScore(low) = %v; want 0", got)
	}
	if got := statusClass(204); got != "2xx" {
		t.Fatalf("statusClass(204) = %q; want 2xx", got)
	}
	if got := statusClass(600); got != "5xx" {
		t.Fatalf("statusClass(600) = %q; want 5xx", got)
	}
}

func TestNoopDiscoverabilityMetrics_ConsumesCalls(t *testing.T) {
	metrics := NoopDiscoverabilityMetrics()
	assessor, ok := metrics.(AssessorMetrics)
	if !ok {
		t.Fatal("NoopDiscoverabilityMetrics does not implement AssessorMetrics")
	}

	metrics.ObserveEmbeddingLatency(1, "ok")
	metrics.IncEmbeddingError("timeout")
	metrics.ObserveRecallLatency(1)
	metrics.ObserveRecall(1, 2, "ok")
	metrics.ObserveRecallFeedback(RecallFeedback{Used: true, AnswerSupported: true, Quality: "high"})
	metrics.ObserveDreamFeedback(DreamFeedback{Decision: "reinforce", Outcome: "ok", FromStatus: "proposed"})
	metrics.ObserveConflictReviewDuration(1, "completed")
	metrics.IncVerifyVerdict("verified")
	assessor.ObserveAssessorCall(10, 5, 1, "ok")
	assessor.IncAssessorValidationFailure("response")
	assessor.IncAssessorValidationFieldFailure("response_contract", "request_id")
	assessor.IncAssessorCandidateTruncation()
	assessor.IncAssessorAssessmentPersistence("persisted")
	assessor.IncAssessorDuplicateRequestPrevention("post_persist")
	assessor.IncAssessorConfidenceGate("meets_write_threshold")
	assessor.IncAssessorTerminalFailure("assessment")
}

func TestAssessorMetricHelpersRecordInMemorySamples(t *testing.T) {
	metrics := NewInMemoryDiscoverabilityMetrics()
	RecordAssessorCall(metrics, 200, 50, 0.25, "ok")
	RecordAssessorValidationFailure(metrics, "response")
	RecordAssessorValidationFieldFailure(metrics, "response_contract", "request_id")
	RecordAssessorCandidateTruncation(metrics)
	RecordAssessorAssessmentPersistence(metrics, "persisted")
	RecordAssessorDuplicateRequestPrevention(metrics, "post_persist")
	RecordAssessorConfidenceGate(metrics, "below_write_threshold")
	RecordAssessorTerminalFailure(metrics, "assessment")

	calls := metrics.AssessorCalls()
	if len(calls) != 1 {
		t.Fatalf("AssessorCalls() len = %d, want 1", len(calls))
	}
	if got, want := calls[0], (AssessorCallSample{InputTokens: 200, OutputTokens: 50, DurationSeconds: 0.25, Outcome: "ok"}); got != want {
		t.Fatalf("AssessorCalls()[0] = %#v, want %#v", got, want)
	}
	if got := metrics.AssessorValidationFailureCount("response"); got != 1 {
		t.Fatalf("AssessorValidationFailureCount() = %d, want 1", got)
	}
	if got := metrics.AssessorValidationFieldFailureCount("response_contract", "request_id"); got != 1 {
		t.Fatalf("AssessorValidationFieldFailureCount() = %d, want 1", got)
	}
	if got := metrics.AssessorCandidateTruncationCount(); got != 1 {
		t.Fatalf("AssessorCandidateTruncationCount() = %d, want 1", got)
	}
	if got := metrics.AssessorPersistenceCount("persisted"); got != 1 {
		t.Fatalf("AssessorPersistenceCount() = %d, want 1", got)
	}
	if got := metrics.AssessorDuplicatePreventionCount("post_persist"); got != 1 {
		t.Fatalf("AssessorDuplicatePreventionCount() = %d, want 1", got)
	}
	if got := metrics.AssessorConfidenceGateCount("below_write_threshold"); got != 1 {
		t.Fatalf("AssessorConfidenceGateCount() = %d, want 1", got)
	}
	if got := metrics.AssessorTerminalFailureCount("assessment"); got != 1 {
		t.Fatalf("AssessorTerminalFailureCount() = %d, want 1", got)
	}
	if got := NormalizeAssessorTerminalFailureStage("unexpected stage"); got != "unknown" {
		t.Fatalf("NormalizeAssessorTerminalFailureStage() = %q, want unknown", got)
	}
	for _, stage := range []string{"predicate_catalog", "extraction", "preflight"} {
		if got := NormalizeAssessorTerminalFailureStage(stage); got != stage {
			t.Fatalf("NormalizeAssessorTerminalFailureStage(%q) = %q, want %q", stage, got, stage)
		}
	}
	for _, retired := range []string{"placement_load", "placement_item"} {
		if got := NormalizeAssessorTerminalFailureStage(retired); got != "unknown" {
			t.Fatalf("NormalizeAssessorTerminalFailureStage(%q) = %q, want unknown", retired, got)
		}
	}

	RecordAssessorCall(NoopDiscoverabilityMetrics(), 0, 0, 0, "")
	RecordAssessorValidationFailure(NoopDiscoverabilityMetrics(), "")
	RecordAssessorValidationFieldFailure(NoopDiscoverabilityMetrics(), "", "")
	RecordAssessorCandidateTruncation(NoopDiscoverabilityMetrics())
	RecordAssessorAssessmentPersistence(NoopDiscoverabilityMetrics(), "")
	RecordAssessorDuplicateRequestPrevention(NoopDiscoverabilityMetrics(), "")
	RecordAssessorConfidenceGate(NoopDiscoverabilityMetrics(), "")
	RecordAssessorTerminalFailure(NoopDiscoverabilityMetrics(), "")
}
func scrapePrometheusMetrics(t *testing.T, metrics *PrometheusMetrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metrics.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d; want 200", rec.Code)
	}
	return rec.Body.String()
}

func requirePrometheusMetricLabels(t *testing.T, body, metric string, labels ...string) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, metric+"{") {
			continue
		}
		matched := true
		for _, label := range labels {
			if !strings.Contains(line, label) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("scraped metrics missing %s label tuple %v\n%s", metric, labels, body)
}

func prometheusCounterValue(t *testing.T, body, metric string, labels ...string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, metric+"{") {
			continue
		}
		matched := true
		for _, label := range labels {
			if !strings.Contains(line, label) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		value, err := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64)
		if err != nil {
			t.Fatalf("parse %s value from %q: %v", metric, line, err)
		}
		return value
	}
	t.Fatalf("scraped metrics missing %s label tuple %v\n%s", metric, labels, body)
	return 0
}

func exerciseNilMetricHelpers(ctx context.Context) {
	RecordEmbeddingLatency(ctx, nil, "model", 1, "ok")
	RecordEmbeddingError(ctx, nil, "model", "error")
	RecordEmbeddingTokens(ctx, nil, "model", 1, 2)
	RecordVerifierLatency(ctx, nil, "model", 1, "ok")
	RecordVerifierTokens(ctx, nil, "model", 1, 1, 2)
	RecordVerifyVerdict(ctx, nil, "model", "verified")
	RecordRecallLatency(ctx, nil, 1)
	RecordRecall(ctx, nil, 1, 1, "ok")
	RecordRecallFeedback(ctx, nil, RecallFeedback{Quality: "high"})
	RecordDreamFeedback(ctx, nil, DreamFeedback{Decision: "reinforce", Outcome: "ok", FromStatus: "proposed"})
	RecordConflictReviewDuration(ctx, nil, 1, "completed")
}
