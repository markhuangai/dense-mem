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

func TestPrometheusMetrics_RecordsScopedMetrics(t *testing.T) {
	metrics := NewPrometheusMetrics()
	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID:      teamID,
		TeamName:    "Research",
		ProfileID:   profileID,
		ProfileName: "Profile A",
	})

	metrics.ObserveHTTPRequest(ctx, "/api/v1/fragments/:id", "get", http.StatusTeapot, 15*time.Millisecond)
	metrics.ObserveEmbeddingLatencyFor(ctx, "embed-model", 123, "ok")
	metrics.IncEmbeddingErrorFor(ctx, "embed-model", "timeout")
	metrics.ObserveEmbeddingTokens(ctx, "embed-model", 11, 22)
	metrics.ObserveVerifierLatencyFor(ctx, "verify-model", 321, "ok")
	metrics.ObserveVerifierTokens(ctx, "verify-model", 7, 8, 15)
	metrics.IncVerifyVerdictFor(ctx, "verify-model", "verified")
	metrics.ObserveRecallLatencyFor(ctx, 42)
	metrics.IncFragmentCreateFor(ctx, "created")
	metrics.IncClaimCreateFor(ctx, "duplicate", "content_hash")
	metrics.IncPromotionOutcomeFor(ctx, "promoted")
	metrics.ObservePromoteLockWaitFor(ctx, 0.25)
	metrics.IncFragmentRetractFor(ctx)
	metrics.IncFactNeedsRevalidationFor(ctx)
	metrics.IncCommunityDetectFor(ctx, "ok")
	metrics.ObserveCommunityDetectFor(ctx, 1.25, 1234)

	body := scrapePrometheusMetrics(t, metrics)
	for _, want := range []string{
		`densemem_http_requests_total{`,
		`team_id="11111111-1111-4111-8111-111111111111"`,
		`profile_id="22222222-2222-4222-8222-222222222222"`,
		`route="/api/v1/fragments/:id"`,
		`method="GET"`,
		`status_class="4xx"`,
		`densemem_embedding_tokens_total{`,
		`model="embed-model"`,
		`kind="prompt"`,
		`kind="total"`,
		`densemem_verifier_tokens_total{`,
		`model="verify-model"`,
		`kind="completion"`,
		`densemem_verify_verdict_total{`,
		`outcome="verified"`,
		`densemem_claim_create_total{`,
		`dedupe_reason="content_hash"`,
		`densemem_retractions_total{`,
		`entity="fragment"`,
		`densemem_community_detect_projected_nodes_bucket{`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scraped metrics missing %q\n%s", want, body)
		}
	}
	for _, blocked := range []string{
		`team_name=`,
		`profile_name=`,
		`Research`,
		`Profile A`,
	} {
		if strings.Contains(body, blocked) {
			t.Fatalf("scraped metrics leaked %q\n%s", blocked, body)
		}
	}
}

func TestPrometheusMetrics_RecordsUnknownLabelsAndFallbackHelpers(t *testing.T) {
	metrics := NewPrometheusMetrics()
	ctx := context.Background()

	metrics.ObserveHTTPRequest(ctx, "", "", 99, time.Millisecond)
	metrics.ObserveEmbeddingLatency(1, "")
	metrics.IncEmbeddingError("")
	metrics.ObserveRecallLatency(2)
	metrics.IncFragmentCreate("")
	metrics.IncClaimCreate("", "")
	metrics.IncVerifyVerdict("")
	metrics.IncPromotionOutcome("")
	metrics.ObservePromoteLockWait(0.1)
	metrics.IncFragmentRetract()
	metrics.IncFactNeedsRevalidation()
	metrics.IncCommunityDetect("")
	metrics.ObserveCommunityDetect(0.5, 0)

	RecordEmbeddingLatency(ctx, metrics, "embed-model", 1, "ok")
	RecordEmbeddingError(ctx, metrics, "embed-model", "error")
	RecordEmbeddingTokens(ctx, metrics, "embed-model", 1, 2)
	RecordVerifierLatency(ctx, metrics, "verify-model", 1, "ok")
	RecordVerifierTokens(ctx, metrics, "verify-model", 1, 1, 2)
	RecordVerifyVerdict(ctx, metrics, "verify-model", "verified")
	RecordRecallLatency(ctx, metrics, 1)
	RecordFragmentCreate(ctx, metrics, "created")
	RecordClaimCreate(ctx, metrics, "created", "")
	RecordPromotionOutcome(ctx, metrics, "promoted")
	RecordPromoteLockWait(ctx, metrics, 0.1)
	RecordFragmentRetract(ctx, metrics)
	RecordFactNeedsRevalidation(ctx, metrics)

	fallback := NewInMemoryDiscoverabilityMetrics()
	RecordEmbeddingLatency(ctx, fallback, "ignored", 10, "ok")
	RecordEmbeddingError(ctx, fallback, "ignored", "timeout")
	RecordEmbeddingTokens(ctx, fallback, "ignored", 1, 2)
	RecordVerifierLatency(ctx, fallback, "ignored", 20, "ok")
	RecordVerifierTokens(ctx, fallback, "ignored", 1, 1, 2)
	RecordVerifyVerdict(ctx, fallback, "ignored", "verified")
	RecordRecallLatency(ctx, fallback, 30)
	RecordFragmentCreate(ctx, fallback, "created")
	RecordClaimCreate(ctx, fallback, "duplicate", "exact")
	RecordPromotionOutcome(ctx, fallback, "promoted")
	RecordPromoteLockWait(ctx, fallback, 0.2)
	RecordFragmentRetract(ctx, fallback)
	RecordFactNeedsRevalidation(ctx, fallback)

	assertFallbackRecorded(t, fallback)
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
		`dedupe_reason="unknown"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scraped metrics missing %q\n%s", want, body)
		}
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
	if got := statusClass(204); got != "2xx" {
		t.Fatalf("statusClass(204) = %q; want 2xx", got)
	}
	if got := statusClass(600); got != "5xx" {
		t.Fatalf("statusClass(600) = %q; want 5xx", got)
	}

	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamName:    "  Team  ",
		ProfileName: "  Profile  ",
	})
	got := identityValues(ctx)
	want := []string{"unknown", "unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("identityValues[%d] = %q; want %q", i, got[i], want[i])
		}
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

func assertFallbackRecorded(t *testing.T, metrics *InMemoryDiscoverabilityMetrics) {
	t.Helper()

	if got := len(metrics.EmbeddingSamples()); got != 1 {
		t.Fatalf("fallback embedding samples = %d; want 1", got)
	}
	if got := metrics.EmbeddingErrorCount("timeout"); got != 1 {
		t.Fatalf("fallback embedding timeout errors = %d; want 1", got)
	}
	if got := metrics.VerifyVerdictCount("verified"); got != 1 {
		t.Fatalf("fallback verified verdicts = %d; want 1", got)
	}
	if got := len(metrics.RecallLatencies()); got != 1 {
		t.Fatalf("fallback recall latencies = %d; want 1", got)
	}
	if got := metrics.FragmentCreateCount("created"); got != 1 {
		t.Fatalf("fallback fragment creates = %d; want 1", got)
	}
	if got := len(metrics.ClaimCreateSamples()); got != 1 {
		t.Fatalf("fallback claim creates = %d; want 1", got)
	}
	if got := metrics.PromotionOutcomeCount("promoted"); got != 1 {
		t.Fatalf("fallback promotions = %d; want 1", got)
	}
	if got := len(metrics.PromoteLockWaits()); got != 1 {
		t.Fatalf("fallback promote lock waits = %d; want 1", got)
	}
	if got := metrics.FragmentRetractCount(); got != 1 {
		t.Fatalf("fallback fragment retracts = %d; want 1", got)
	}
	if got := metrics.FactNeedsRevalidationCount(); got != 1 {
		t.Fatalf("fallback revalidation count = %d; want 1", got)
	}
}

func exerciseNilMetricHelpers(ctx context.Context) {
	RecordEmbeddingLatency(ctx, nil, "model", 1, "ok")
	RecordEmbeddingError(ctx, nil, "model", "error")
	RecordEmbeddingTokens(ctx, nil, "model", 1, 2)
	RecordVerifierLatency(ctx, nil, "model", 1, "ok")
	RecordVerifierTokens(ctx, nil, "model", 1, 1, 2)
	RecordVerifyVerdict(ctx, nil, "model", "verified")
	RecordRecallLatency(ctx, nil, 1)
	RecordFragmentCreate(ctx, nil, "created")
	RecordClaimCreate(ctx, nil, "created", "")
	RecordPromotionOutcome(ctx, nil, "promoted")
	RecordPromoteLockWait(ctx, nil, 0.1)
	RecordFragmentRetract(ctx, nil)
	RecordFactNeedsRevalidation(ctx, nil)
}
