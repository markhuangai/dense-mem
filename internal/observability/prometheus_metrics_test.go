package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID:      teamID,
		TeamName:    "Research",
		ProfileID:   profileID,
		ProfileName: "Profile A",
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
