package observability

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const unknownMetricLabel = "unknown"

// HTTPMetrics records authenticated HTTP request metrics.
type HTTPMetrics interface {
	ObserveHTTPRequest(ctx context.Context, route, method string, status int, duration time.Duration)
}

// ScopedDiscoverabilityMetrics records domain metrics with request identity.
type ScopedDiscoverabilityMetrics interface {
	DiscoverabilityMetrics
	ObserveEmbeddingLatencyFor(ctx context.Context, model string, durationMs float64, outcome string)
	IncEmbeddingErrorFor(ctx context.Context, model string, code string)
	ObserveEmbeddingTokens(ctx context.Context, model string, promptTokens, totalTokens int64)
	ObserveVerifierLatencyFor(ctx context.Context, model string, durationMs float64, outcome string)
	ObserveVerifierTokens(ctx context.Context, model string, promptTokens, completionTokens, totalTokens int64)
	IncVerifyVerdictFor(ctx context.Context, model string, outcome string)
	ObserveRecallLatencyFor(ctx context.Context, durationMs float64)
	ObserveRecallFor(ctx context.Context, durationMs float64, resultCount int, outcome string)
	ObserveRecallFeedbackFor(ctx context.Context, feedback RecallFeedback)
	ObserveDreamFeedbackFor(ctx context.Context, feedback DreamFeedback)
	ObserveMemoryFunnelLatencyFor(ctx context.Context, stage string, seconds float64, outcome string)
	IncFragmentCreateFor(ctx context.Context, outcome string)
	IncClaimCreateFor(ctx context.Context, outcome string, dedupeReason string)
	IncPromotionOutcomeFor(ctx context.Context, outcome string)
	ObservePromoteLockWaitFor(ctx context.Context, seconds float64)
	IncFragmentRetractFor(ctx context.Context)
	IncFactNeedsRevalidationFor(ctx context.Context)
	IncCommunityDetectFor(ctx context.Context, outcome string)
	ObserveCommunityDetectFor(ctx context.Context, durationSeconds float64, projectedNodes int)
}

// PrometheusMetrics exports Dense-Mem operational metrics through a private
// registry. It is safe for concurrent use.
type PrometheusMetrics struct {
	registry *prometheus.Registry

	httpRequests                 *prometheus.CounterVec
	httpDuration                 *prometheus.HistogramVec
	embeddingCalls               *prometheus.CounterVec
	embeddingErrors              *prometheus.CounterVec
	embeddingDur                 *prometheus.HistogramVec
	embeddingTokens              *prometheus.CounterVec
	verifierCalls                *prometheus.CounterVec
	verifierDur                  *prometheus.HistogramVec
	verifierTokens               *prometheus.CounterVec
	recallCalls                  *prometheus.CounterVec
	recallDur                    *prometheus.HistogramVec
	recallResults                *prometheus.HistogramVec
	recallFeedback               *prometheus.CounterVec
	recallQuality                *prometheus.HistogramVec
	dreamFeedback                *prometheus.CounterVec
	memoryFunnel                 *prometheus.HistogramVec
	fragmentCreates              *prometheus.CounterVec
	claimCreates                 *prometheus.CounterVec
	verifyVerdicts               *prometheus.CounterVec
	promotions                   *prometheus.CounterVec
	promoteWait                  *prometheus.HistogramVec
	retractions                  *prometheus.CounterVec
	revalidation                 *prometheus.CounterVec
	communityRuns                *prometheus.CounterVec
	communityDur                 *prometheus.HistogramVec
	communityNodes               *prometheus.HistogramVec
	assessorCalls                *prometheus.CounterVec
	assessorDur                  *prometheus.HistogramVec
	assessorTokens               *prometheus.CounterVec
	assessorValidation           *prometheus.CounterVec
	assessorCandidateTruncations *prometheus.CounterVec
	assessorPersistence          *prometheus.CounterVec
	assessorDuplicatePrevention  *prometheus.CounterVec
	assessorConfidenceGate       *prometheus.CounterVec
	assessorReviewExpiry         prometheus.Counter
}

var _ DiscoverabilityMetrics = (*PrometheusMetrics)(nil)
var _ ScopedDiscoverabilityMetrics = (*PrometheusMetrics)(nil)
var _ HTTPMetrics = (*PrometheusMetrics)(nil)
var _ AssessorMetrics = (*PrometheusMetrics)(nil)

// NewPrometheusMetrics creates a Prometheus recorder with a private registry so
// tests and multiple server instances do not collide with global collectors.
func NewPrometheusMetrics() *PrometheusMetrics {
	m := &PrometheusMetrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_http_requests_total",
			Help: "Authenticated HTTP requests handled by Dense-Mem.",
		}, append(identityLabels(), "route", "method", "status_class")),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_http_request_duration_seconds",
			Help:    "Authenticated HTTP request duration.",
			Buckets: prometheus.DefBuckets,
		}, append(identityLabels(), "route", "method", "status_class")),
		embeddingCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_embedding_requests_total",
			Help: "Embedding provider calls by outcome.",
		}, append(identityLabels(), "model", "outcome")),
		embeddingErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_embedding_errors_total",
			Help: "Embedding provider errors by code.",
		}, append(identityLabels(), "model", "code")),
		embeddingDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_embedding_duration_seconds",
			Help:    "Embedding provider request duration.",
			Buckets: prometheus.DefBuckets,
		}, append(identityLabels(), "model", "outcome")),
		embeddingTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_embedding_tokens_total",
			Help: "Embedding provider token usage when reported by the provider.",
		}, append(identityLabels(), "model", "kind")),
		verifierCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_verifier_requests_total",
			Help: "Verifier AI calls by outcome.",
		}, append(identityLabels(), "model", "outcome")),
		verifierDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_verifier_duration_seconds",
			Help:    "Verifier AI request duration.",
			Buckets: prometheus.DefBuckets,
		}, append(identityLabels(), "model", "outcome")),
		verifierTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_verifier_tokens_total",
			Help: "Verifier AI token usage when reported by the provider.",
		}, append(identityLabels(), "model", "kind")),
		recallCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_recall_requests_total",
			Help: "Recall requests by outcome.",
		}, append(identityLabels(), "outcome")),
		recallDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_recall_duration_seconds",
			Help:    "Recall request duration.",
			Buckets: prometheus.DefBuckets,
		}, identityLabels()),
		recallResults: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_recall_results",
			Help:    "Recall result count per request.",
			Buckets: []float64{0, 1, 2, 5, 10, 20, 50, 100},
		}, append(identityLabels(), "outcome")),
		recallFeedback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_recall_feedback_total",
			Help: "Host LLM online recall feedback events with bounded judgment labels.",
		}, append(identityLabels(), "used", "answer_supported", "quality", "missing_context", "irrelevant")),
		recallQuality: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_recall_feedback_quality_score",
			Help:    "Numeric host LLM online recall quality score.",
			Buckets: []float64{0, 0.5, 1},
		}, identityLabels()),
		dreamFeedback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_dream_feedback_total",
			Help: "Dream feedback decision events with bounded labels.",
		}, append(identityLabels(), "decision", "outcome", "from_status")),
		memoryFunnel: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_memory_funnel_latency_seconds",
			Help:    "Latency between memory pipeline stages.",
			Buckets: []float64{1, 5, 30, 60, 300, 900, 3600, 21600, 86400, 604800},
		}, append(identityLabels(), "stage", "outcome")),
		fragmentCreates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_fragment_create_total",
			Help: "Fragment create outcomes.",
		}, append(identityLabels(), "outcome")),
		claimCreates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_claim_create_total",
			Help: "Claim create outcomes.",
		}, append(identityLabels(), "outcome", "dedupe_reason")),
		verifyVerdicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_verify_verdict_total",
			Help: "Claim verification outcomes.",
		}, append(identityLabels(), "model", "outcome")),
		promotions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_promotion_outcome_total",
			Help: "Claim-to-fact promotion outcomes.",
		}, append(identityLabels(), "outcome")),
		promoteWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_promote_lock_wait_seconds",
			Help:    "Promotion advisory lock wait duration.",
			Buckets: prometheus.DefBuckets,
		}, identityLabels()),
		retractions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_retractions_total",
			Help: "Knowledge retractions by entity type.",
		}, append(identityLabels(), "entity")),
		revalidation: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_fact_needs_revalidation_total",
			Help: "Facts queued for revalidation after contradicting evidence.",
		}, identityLabels()),
		communityRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_community_detect_total",
			Help: "Community detection runs by outcome.",
		}, append(identityLabels(), "outcome")),
		communityDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_community_detect_duration_seconds",
			Help:    "Community detection duration.",
			Buckets: prometheus.DefBuckets,
		}, identityLabels()),
		communityNodes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_community_detect_projected_nodes",
			Help:    "Projected node count for community detection runs.",
			Buckets: []float64{100, 1000, 10000, 100000, 500000, 1000000},
		}, identityLabels()),
		assessorCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_assessor_requests_total",
			Help: "Integrated assessor provider calls by bounded outcome.",
		}, []string{"outcome"}),
		assessorDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_assessor_duration_seconds",
			Help:    "Integrated assessor provider call duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome"}),
		assessorTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_assessor_tokens_total",
			Help: "Server-accounted integrated assessor tokens.",
		}, []string{"kind"}),
		assessorValidation: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_assessor_validation_failures_total",
			Help: "Integrated assessor validation failures by bounded stage.",
		}, []string{"stage"}),
		assessorCandidateTruncations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_assessor_candidate_truncations_total",
			Help: "Integrated assessor requests with token-bounded candidate context.",
		}, nil),
		assessorPersistence: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_assessor_assessment_persistence_total",
			Help: "Append-once assessment persistence and reuse outcomes.",
		}, []string{"outcome"}),
		assessorDuplicatePrevention: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_assessor_duplicate_request_prevented_total",
			Help: "Provider calls prevented by persisted assessments or durable reservations.",
		}, []string{"stage"}),
		assessorConfidenceGate: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_assessor_confidence_gate_total",
			Help: "Integrated assessor confidence gate bands.",
		}, []string{"band"}),
		assessorReviewExpiry: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "densemem_assessor_review_expiry_total",
			Help: "Integrated assessor semantic review tasks expired by the scheduler.",
		}),
	}
	m.registry.MustRegister(
		m.httpRequests, m.httpDuration,
		m.embeddingCalls, m.embeddingErrors, m.embeddingDur, m.embeddingTokens,
		m.verifierCalls, m.verifierDur, m.verifierTokens,
		m.recallCalls, m.recallDur, m.recallResults,
		m.recallFeedback, m.recallQuality, m.dreamFeedback, m.memoryFunnel,
		m.fragmentCreates, m.claimCreates, m.verifyVerdicts, m.promotions,
		m.promoteWait, m.retractions, m.revalidation,
		m.communityRuns, m.communityDur, m.communityNodes,
		m.assessorCalls, m.assessorDur, m.assessorTokens, m.assessorValidation,
		m.assessorCandidateTruncations, m.assessorPersistence, m.assessorDuplicatePrevention,
		m.assessorConfidenceGate, m.assessorReviewExpiry,
	)
	return m
}

func (m *PrometheusMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *PrometheusMetrics) ObserveHTTPRequest(ctx context.Context, route, method string, status int, duration time.Duration) {
	labels := identityValues(ctx)
	labels = append(labels, normalizeLabel(route), strings.ToUpper(normalizeLabel(method)), statusClass(status))
	m.httpRequests.WithLabelValues(labels...).Inc()
	m.httpDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func (m *PrometheusMetrics) ObserveEmbeddingLatency(durationMs float64, outcome string) {
	m.ObserveEmbeddingLatencyFor(context.Background(), unknownMetricLabel, durationMs, outcome)
}

func (m *PrometheusMetrics) ObserveEmbeddingLatencyFor(ctx context.Context, model string, durationMs float64, outcome string) {
	labels := append(identityValues(ctx), normalizeLabel(model), normalizeLabel(outcome))
	m.embeddingCalls.WithLabelValues(labels...).Inc()
	m.embeddingDur.WithLabelValues(labels...).Observe(durationMs / 1000)
}

func (m *PrometheusMetrics) IncEmbeddingError(code string) {
	m.IncEmbeddingErrorFor(context.Background(), unknownMetricLabel, code)
}

func (m *PrometheusMetrics) IncEmbeddingErrorFor(ctx context.Context, model string, code string) {
	m.embeddingErrors.WithLabelValues(append(identityValues(ctx), normalizeLabel(model), normalizeLabel(code))...).Inc()
}

func (m *PrometheusMetrics) ObserveEmbeddingTokens(ctx context.Context, model string, promptTokens, totalTokens int64) {
	m.addTokens(m.embeddingTokens, ctx, model, promptTokens, 0, totalTokens)
}

func (m *PrometheusMetrics) ObserveVerifierLatencyFor(ctx context.Context, model string, durationMs float64, outcome string) {
	labels := append(identityValues(ctx), normalizeLabel(model), normalizeLabel(outcome))
	m.verifierCalls.WithLabelValues(labels...).Inc()
	m.verifierDur.WithLabelValues(labels...).Observe(durationMs / 1000)
}

func (m *PrometheusMetrics) ObserveVerifierTokens(ctx context.Context, model string, promptTokens, completionTokens, totalTokens int64) {
	m.addTokens(m.verifierTokens, ctx, model, promptTokens, completionTokens, totalTokens)
}

func (m *PrometheusMetrics) ObserveRecallLatency(durationMs float64) {
	m.ObserveRecallLatencyFor(context.Background(), durationMs)
}

func (m *PrometheusMetrics) ObserveRecallLatencyFor(ctx context.Context, durationMs float64) {
	labels := identityValues(ctx)
	m.recallCalls.WithLabelValues(append(labels, "ok")...).Inc()
	m.recallDur.WithLabelValues(labels...).Observe(durationMs / 1000)
}

func (m *PrometheusMetrics) ObserveRecall(durationMs float64, resultCount int, outcome string) {
	m.ObserveRecallFor(context.Background(), durationMs, resultCount, outcome)
}

func (m *PrometheusMetrics) ObserveRecallFor(ctx context.Context, durationMs float64, resultCount int, outcome string) {
	labels := identityValues(ctx)
	outcome = normalizeLabel(outcome)
	m.recallCalls.WithLabelValues(append(labels, outcome)...).Inc()
	m.recallDur.WithLabelValues(labels...).Observe(durationMs / 1000)
	if resultCount < 0 {
		resultCount = 0
	}
	m.recallResults.WithLabelValues(append(labels, outcome)...).Observe(float64(resultCount))
}

func (m *PrometheusMetrics) ObserveRecallFeedback(feedback RecallFeedback) {
	m.ObserveRecallFeedbackFor(context.Background(), feedback)
}

func (m *PrometheusMetrics) ObserveRecallFeedbackFor(ctx context.Context, feedback RecallFeedback) {
	quality := normalizeRecallFeedbackQuality(feedback.Quality)
	labels := append(identityValues(ctx),
		boolLabel(feedback.Used),
		boolLabel(feedback.AnswerSupported),
		quality,
		boolLabel(feedback.MissingContext),
		boolLabel(feedback.Irrelevant),
	)
	m.recallFeedback.WithLabelValues(labels...).Inc()
	m.recallQuality.WithLabelValues(identityValues(ctx)...).Observe(recallFeedbackQualityScore(quality))
}

func (m *PrometheusMetrics) ObserveDreamFeedback(feedback DreamFeedback) {
	m.ObserveDreamFeedbackFor(context.Background(), feedback)
}

func (m *PrometheusMetrics) ObserveDreamFeedbackFor(ctx context.Context, feedback DreamFeedback) {
	labels := append(identityValues(ctx),
		normalizeDreamFeedbackDecision(feedback.Decision),
		normalizeDreamFeedbackOutcome(feedback.Outcome),
		normalizeDreamStatusLabel(feedback.FromStatus),
	)
	m.dreamFeedback.WithLabelValues(labels...).Inc()
}

func (m *PrometheusMetrics) ObserveMemoryFunnelLatency(stage string, seconds float64, outcome string) {
	m.ObserveMemoryFunnelLatencyFor(context.Background(), stage, seconds, outcome)
}

func (m *PrometheusMetrics) ObserveMemoryFunnelLatencyFor(ctx context.Context, stage string, seconds float64, outcome string) {
	if seconds < 0 {
		return
	}
	m.memoryFunnel.WithLabelValues(append(identityValues(ctx), normalizeLabel(stage), normalizeLabel(outcome))...).Observe(seconds)
}

func (m *PrometheusMetrics) IncFragmentCreate(outcome string) {
	m.IncFragmentCreateFor(context.Background(), outcome)
}

func (m *PrometheusMetrics) IncFragmentCreateFor(ctx context.Context, outcome string) {
	m.fragmentCreates.WithLabelValues(append(identityValues(ctx), normalizeLabel(outcome))...).Inc()
}

func (m *PrometheusMetrics) IncClaimCreate(outcome string, dedupeReason string) {
	m.IncClaimCreateFor(context.Background(), outcome, dedupeReason)
}

func (m *PrometheusMetrics) IncClaimCreateFor(ctx context.Context, outcome string, dedupeReason string) {
	m.claimCreates.WithLabelValues(append(identityValues(ctx), normalizeLabel(outcome), normalizeLabel(dedupeReason))...).Inc()
}

func (m *PrometheusMetrics) IncVerifyVerdict(outcome string) {
	m.IncVerifyVerdictFor(context.Background(), unknownMetricLabel, outcome)
}

func (m *PrometheusMetrics) IncVerifyVerdictFor(ctx context.Context, model string, outcome string) {
	m.verifyVerdicts.WithLabelValues(append(identityValues(ctx), normalizeLabel(model), normalizeLabel(outcome))...).Inc()
}

func (m *PrometheusMetrics) IncPromotionOutcome(outcome string) {
	m.IncPromotionOutcomeFor(context.Background(), outcome)
}

func (m *PrometheusMetrics) IncPromotionOutcomeFor(ctx context.Context, outcome string) {
	m.promotions.WithLabelValues(append(identityValues(ctx), normalizeLabel(outcome))...).Inc()
}

func (m *PrometheusMetrics) ObservePromoteLockWait(seconds float64) {
	m.ObservePromoteLockWaitFor(context.Background(), seconds)
}

func (m *PrometheusMetrics) ObservePromoteLockWaitFor(ctx context.Context, seconds float64) {
	m.promoteWait.WithLabelValues(identityValues(ctx)...).Observe(seconds)
}

func (m *PrometheusMetrics) IncFragmentRetract() {
	m.IncFragmentRetractFor(context.Background())
}

func (m *PrometheusMetrics) IncFragmentRetractFor(ctx context.Context) {
	m.retractions.WithLabelValues(append(identityValues(ctx), "fragment")...).Inc()
}

func (m *PrometheusMetrics) IncFactNeedsRevalidation() {
	m.IncFactNeedsRevalidationFor(context.Background())
}

func (m *PrometheusMetrics) IncFactNeedsRevalidationFor(ctx context.Context) {
	m.revalidation.WithLabelValues(identityValues(ctx)...).Inc()
}

func (m *PrometheusMetrics) IncCommunityDetect(outcome string) {
	m.IncCommunityDetectFor(context.Background(), outcome)
}

func (m *PrometheusMetrics) IncCommunityDetectFor(ctx context.Context, outcome string) {
	m.communityRuns.WithLabelValues(append(identityValues(ctx), normalizeLabel(outcome))...).Inc()
}

func (m *PrometheusMetrics) ObserveCommunityDetect(durationSeconds float64, projectedNodes int) {
	m.ObserveCommunityDetectFor(context.Background(), durationSeconds, projectedNodes)
}

func (m *PrometheusMetrics) ObserveCommunityDetectFor(ctx context.Context, durationSeconds float64, projectedNodes int) {
	labels := identityValues(ctx)
	m.communityDur.WithLabelValues(labels...).Observe(durationSeconds)
	m.communityNodes.WithLabelValues(labels...).Observe(float64(projectedNodes))
}

func (m *PrometheusMetrics) ObserveAssessorCall(inputTokens, outputTokens int, durationSeconds float64, outcome string) {
	outcome = normalizeAssessorCallOutcome(outcome)
	m.assessorCalls.WithLabelValues(outcome).Inc()
	if durationSeconds >= 0 {
		m.assessorDur.WithLabelValues(outcome).Observe(durationSeconds)
	}
	if inputTokens > 0 {
		m.assessorTokens.WithLabelValues("input").Add(float64(inputTokens))
	}
	if outputTokens > 0 {
		m.assessorTokens.WithLabelValues("output").Add(float64(outputTokens))
	}
}

func (m *PrometheusMetrics) IncAssessorValidationFailure(stage string) {
	m.assessorValidation.WithLabelValues(normalizeAssessorValidationStage(stage)).Inc()
}

func (m *PrometheusMetrics) IncAssessorCandidateTruncation() {
	m.assessorCandidateTruncations.WithLabelValues().Inc()
}

func (m *PrometheusMetrics) IncAssessorAssessmentPersistence(outcome string) {
	m.assessorPersistence.WithLabelValues(normalizeAssessorPersistenceOutcome(outcome)).Inc()
}

func (m *PrometheusMetrics) IncAssessorDuplicateRequestPrevention(stage string) {
	m.assessorDuplicatePrevention.WithLabelValues(normalizeAssessorDuplicatePreventionStage(stage)).Inc()
}

func (m *PrometheusMetrics) IncAssessorConfidenceGate(band string) {
	m.assessorConfidenceGate.WithLabelValues(normalizeAssessorConfidenceGateBand(band)).Inc()
}

func (m *PrometheusMetrics) AddAssessorReviewExpiry(count int64) {
	if count > 0 {
		m.assessorReviewExpiry.Add(float64(count))
	}
}

func (m *PrometheusMetrics) addTokens(counter *prometheus.CounterVec, ctx context.Context, model string, promptTokens, completionTokens, totalTokens int64) {
	base := append(identityValues(ctx), normalizeLabel(model))
	if promptTokens > 0 {
		counter.WithLabelValues(append(base, "prompt")...).Add(float64(promptTokens))
	}
	if completionTokens > 0 {
		counter.WithLabelValues(append(base, "completion")...).Add(float64(completionTokens))
	}
	if totalTokens > 0 {
		counter.WithLabelValues(append(base, "total")...).Add(float64(totalTokens))
	}
}

func identityLabels() []string {
	return []string{"team_id", "profile_id"}
}

func identityValues(ctx context.Context) []string {
	if actor, ok := requestctx.ActorProfileFromContext(ctx); ok {
		return []string{
			uuidLabel(actor.TeamID),
			uuidLabel(actor.ProfileID),
		}
	}
	return []string{unknownMetricLabel, unknownMetricLabel}
}

func uuidLabel(id uuid.UUID) string {
	if id == uuid.Nil {
		return unknownMetricLabel
	}
	return id.String()
}

func normalizeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return unknownMetricLabel
	}
	return value
}

func statusClass(status int) string {
	if status < 100 || status > 599 {
		status = 500
	}
	return strconv.Itoa(status/100) + "xx"
}

func normalizeRecallFeedbackQuality(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high", "medium", "low":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func recallFeedbackQualityScore(quality string) float64 {
	switch normalizeRecallFeedbackQuality(quality) {
	case "high":
		return 1
	case "medium":
		return 0.5
	case "low":
		return 0
	default:
		return 0
	}
}

func normalizeDreamFeedbackDecision(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ignore", "reinforce", "stale", "reject", "confirm_true", "confirm_false", "promote_candidate":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeDreamFeedbackOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ok", "error":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeDreamStatusLabel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "proposed", "reinforced", "stale", "rejected", "promoted":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func boolLabel(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func normalizeAssessorCallOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ok", "provider_error", "invalid_response":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeAssessorValidationStage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "request", "response", "stored_response":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeAssessorPersistenceOutcome(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "persisted", "reused", "error":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeAssessorDuplicatePreventionStage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "post_persist", "reservation":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

func normalizeAssessorConfidenceGateBand(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "meets_write_threshold", "below_write_threshold", "not_applicable":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return unknownMetricLabel
	}
}

// RecordEmbeddingLatency preserves the narrow DiscoverabilityMetrics contract
// while allowing scoped recorders to attach request identity and model labels.
func RecordEmbeddingLatency(ctx context.Context, metrics DiscoverabilityMetrics, model string, durationMs float64, outcome string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveEmbeddingLatencyFor(ctx, model, durationMs, outcome)
		return
	}
	metrics.ObserveEmbeddingLatency(durationMs, outcome)
}

// RecordEmbeddingError preserves the narrow DiscoverabilityMetrics contract
// while allowing scoped recorders to attach request identity and model labels.
func RecordEmbeddingError(ctx context.Context, metrics DiscoverabilityMetrics, model string, code string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.IncEmbeddingErrorFor(ctx, model, code)
		return
	}
	metrics.IncEmbeddingError(code)
}

// RecordEmbeddingTokens records provider-reported embedding token usage when
// the configured metrics backend supports it.
func RecordEmbeddingTokens(ctx context.Context, metrics DiscoverabilityMetrics, model string, promptTokens, totalTokens int64) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveEmbeddingTokens(ctx, model, promptTokens, totalTokens)
	}
}

// RecordVerifierLatency records verifier provider latency when the configured
// metrics backend supports scoped provider metrics.
func RecordVerifierLatency(ctx context.Context, metrics DiscoverabilityMetrics, model string, durationMs float64, outcome string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveVerifierLatencyFor(ctx, model, durationMs, outcome)
	}
}

// RecordVerifierTokens records provider-reported verifier token usage when
// the configured metrics backend supports it.
func RecordVerifierTokens(ctx context.Context, metrics DiscoverabilityMetrics, model string, promptTokens, completionTokens, totalTokens int64) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveVerifierTokens(ctx, model, promptTokens, completionTokens, totalTokens)
	}
}

// RecordVerifyVerdict preserves the narrow DiscoverabilityMetrics contract
// while allowing scoped recorders to attach request identity and model labels.
func RecordVerifyVerdict(ctx context.Context, metrics DiscoverabilityMetrics, model string, outcome string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.IncVerifyVerdictFor(ctx, model, outcome)
		return
	}
	metrics.IncVerifyVerdict(outcome)
}

func RecordRecallLatency(ctx context.Context, metrics DiscoverabilityMetrics, durationMs float64) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveRecallLatencyFor(ctx, durationMs)
		return
	}
	metrics.ObserveRecallLatency(durationMs)
}

func RecordRecall(ctx context.Context, metrics DiscoverabilityMetrics, durationMs float64, resultCount int, outcome string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveRecallFor(ctx, durationMs, resultCount, outcome)
		return
	}
	metrics.ObserveRecall(durationMs, resultCount, outcome)
}

func RecordRecallFeedback(ctx context.Context, metrics DiscoverabilityMetrics, feedback RecallFeedback) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveRecallFeedbackFor(ctx, feedback)
		return
	}
	metrics.ObserveRecallFeedback(feedback)
}

func RecordDreamFeedback(ctx context.Context, metrics DiscoverabilityMetrics, feedback DreamFeedback) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveDreamFeedbackFor(ctx, feedback)
		return
	}
	metrics.ObserveDreamFeedback(feedback)
}

func RecordMemoryFunnelLatency(ctx context.Context, metrics DiscoverabilityMetrics, stage string, seconds float64, outcome string) {
	if metrics == nil || seconds < 0 {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObserveMemoryFunnelLatencyFor(ctx, stage, seconds, outcome)
		return
	}
	metrics.ObserveMemoryFunnelLatency(stage, seconds, outcome)
}

func RecordFragmentCreate(ctx context.Context, metrics DiscoverabilityMetrics, outcome string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.IncFragmentCreateFor(ctx, outcome)
		return
	}
	metrics.IncFragmentCreate(outcome)
}

func RecordClaimCreate(ctx context.Context, metrics DiscoverabilityMetrics, outcome string, dedupeReason string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.IncClaimCreateFor(ctx, outcome, dedupeReason)
		return
	}
	metrics.IncClaimCreate(outcome, dedupeReason)
}

func RecordPromotionOutcome(ctx context.Context, metrics DiscoverabilityMetrics, outcome string) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.IncPromotionOutcomeFor(ctx, outcome)
		return
	}
	metrics.IncPromotionOutcome(outcome)
}

func RecordPromoteLockWait(ctx context.Context, metrics DiscoverabilityMetrics, seconds float64) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.ObservePromoteLockWaitFor(ctx, seconds)
		return
	}
	metrics.ObservePromoteLockWait(seconds)
}

func RecordFragmentRetract(ctx context.Context, metrics DiscoverabilityMetrics) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.IncFragmentRetractFor(ctx)
		return
	}
	metrics.IncFragmentRetract()
}

func RecordFactNeedsRevalidation(ctx context.Context, metrics DiscoverabilityMetrics) {
	if metrics == nil {
		return
	}
	if scoped, ok := metrics.(ScopedDiscoverabilityMetrics); ok {
		scoped.IncFactNeedsRevalidationFor(ctx)
		return
	}
	metrics.IncFactNeedsRevalidation()
}

func RecordAssessorCall(metrics DiscoverabilityMetrics, inputTokens, outputTokens int, durationSeconds float64, outcome string) {
	if recorder, ok := metrics.(AssessorMetrics); ok {
		recorder.ObserveAssessorCall(inputTokens, outputTokens, durationSeconds, outcome)
	}
}

func RecordAssessorValidationFailure(metrics DiscoverabilityMetrics, stage string) {
	if recorder, ok := metrics.(AssessorMetrics); ok {
		recorder.IncAssessorValidationFailure(stage)
	}
}

func RecordAssessorCandidateTruncation(metrics DiscoverabilityMetrics) {
	if recorder, ok := metrics.(AssessorMetrics); ok {
		recorder.IncAssessorCandidateTruncation()
	}
}

func RecordAssessorAssessmentPersistence(metrics DiscoverabilityMetrics, outcome string) {
	if recorder, ok := metrics.(AssessorMetrics); ok {
		recorder.IncAssessorAssessmentPersistence(outcome)
	}
}

func RecordAssessorDuplicateRequestPrevention(metrics DiscoverabilityMetrics, stage string) {
	if recorder, ok := metrics.(AssessorMetrics); ok {
		recorder.IncAssessorDuplicateRequestPrevention(stage)
	}
}

func RecordAssessorConfidenceGate(metrics DiscoverabilityMetrics, band string) {
	if recorder, ok := metrics.(AssessorMetrics); ok {
		recorder.IncAssessorConfidenceGate(band)
	}
}

func RecordAssessorReviewExpiry(metrics DiscoverabilityMetrics, count int64) {
	if recorder, ok := metrics.(AssessorMetrics); ok {
		recorder.AddAssessorReviewExpiry(count)
	}
}
