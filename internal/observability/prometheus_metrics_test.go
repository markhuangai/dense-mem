package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/prometheus/client_golang/prometheus"

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
	metrics.ObserveRecallFor(ctx, 42, 3, "ok")
	metrics.ObserveMemoryFunnelLatencyFor(ctx, "claim_to_verify", 2.5, "verified")
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
		`densemem_recall_results_bucket{`,
		`densemem_memory_funnel_latency_seconds_bucket{`,
		`stage="claim_to_verify"`,
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
	metrics.ObserveRecall(2, 0, "")
	metrics.ObserveMemoryFunnelLatency("", 1, "")
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
	RecordRecall(ctx, metrics, 1, 2, "ok")
	RecordMemoryFunnelLatency(ctx, metrics, "claim_to_promotion", 5, "promoted")
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
	RecordRecall(ctx, fallback, 30, 4, "ok")
	RecordMemoryFunnelLatency(ctx, fallback, "claim_to_verify", 3, "verified")
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

func TestKnowledgeBacklogCollector_ExportsScopedBacklogGauges(t *testing.T) {
	reader := &fakeKnowledgeBacklogReader{samples: []knowledgeBacklogSample{
		{TeamID: "team-1", ProfileID: "profile-1", Item: "claim_candidate", Count: 3},
		{TeamID: "team-1", ProfileID: "profile-1", Item: "claim_validated", Count: 2},
		{TeamID: "team-1", ProfileID: "profile-2", Item: "claim_disputed", Count: 1},
		{TeamID: "team-1", ProfileID: "profile-1", Item: "fact_needs_revalidation", Count: 4},
		{TeamID: "team-1", ProfileID: "profile-1", Item: "unbounded", Count: 999},
		{TeamID: "team-1", ProfileID: "profile-negative", Item: "claim_candidate", Count: -1},
	}}
	metrics := NewPrometheusMetrics()
	metrics.RegisterKnowledgeBacklogCollector(reader, nil)

	body := scrapePrometheusMetrics(t, metrics)

	for _, want := range []string{
		`densemem_knowledge_backlog_items{item="claim_candidate",profile_id="profile-1",team_id="team-1"} 3`,
		`densemem_knowledge_backlog_items{item="claim_validated",profile_id="profile-1",team_id="team-1"} 2`,
		`densemem_knowledge_backlog_items{item="claim_disputed",profile_id="profile-2",team_id="team-1"} 1`,
		`densemem_knowledge_backlog_items{item="fact_needs_revalidation",profile_id="profile-1",team_id="team-1"} 4`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("scraped metrics missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "unbounded") {
		t.Fatalf("scraped metrics included unbounded item\n%s", body)
	}
	if strings.Contains(body, "profile-negative") {
		t.Fatalf("scraped metrics included negative sample\n%s", body)
	}
}

func TestKnowledgeBacklogCollector_UsesCachedSamplesOnReadFailure(t *testing.T) {
	reader := &fakeKnowledgeBacklogReader{samples: []knowledgeBacklogSample{
		{TeamID: "team-1", ProfileID: "profile-1", Item: "claim_candidate", Count: 3},
	}}
	collector := NewKnowledgeBacklogCollector(reader, nil)
	collector.ttl = 0

	samples, err := collector.samples()
	if err != nil {
		t.Fatalf("initial samples error: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("initial samples = %+v", samples)
	}

	reader.err = errors.New("neo4j unavailable")
	samples, err = collector.samples()
	if err != nil {
		t.Fatalf("cached samples error: %v", err)
	}
	if len(samples) != 1 || samples[0].Count != 3 {
		t.Fatalf("cached samples = %+v", samples)
	}
}

func TestKnowledgeBacklogCollector_UsesFreshCacheWithoutReader(t *testing.T) {
	reader := &fakeKnowledgeBacklogReader{samples: []knowledgeBacklogSample{
		{TeamID: "team-1", ProfileID: "profile-1", Item: "claim_candidate", Count: 3},
	}}
	collector := NewKnowledgeBacklogCollector(reader, nil)
	collector.ttl = time.Hour

	samples, err := collector.samples()
	if err != nil {
		t.Fatalf("initial samples error: %v", err)
	}
	if len(samples) != 1 || samples[0].Count != 3 {
		t.Fatalf("initial samples = %+v", samples)
	}

	reader.err = errors.New("reader should not be called while cache is fresh")
	samples, err = collector.samples()
	if err != nil {
		t.Fatalf("fresh cached samples error: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d; want 1", reader.calls)
	}
	if len(samples) != 1 || samples[0].Count != 3 {
		t.Fatalf("fresh cached samples = %+v", samples)
	}

	samples[0].Count = 99
	samples, err = collector.samples()
	if err != nil {
		t.Fatalf("fresh cached samples after mutation error: %v", err)
	}
	if samples[0].Count != 3 {
		t.Fatalf("cache returned mutated sample = %+v", samples)
	}
}

func TestKnowledgeBacklogCollector_ReturnsErrorForUnexpectedReadResult(t *testing.T) {
	collector := NewKnowledgeBacklogCollector(&fakeKnowledgeBacklogReader{
		useResult: true,
		result:    "not samples",
	}, nil)

	_, err := collector.samples()
	if err == nil || !strings.Contains(err.Error(), "knowledge backlog query returned string") {
		t.Fatalf("samples error = %v", err)
	}
}

func TestKnowledgeBacklogCollector_CollectWarnsOnReadFailure(t *testing.T) {
	logger := &fakeLogProvider{}
	collector := NewKnowledgeBacklogCollector(&fakeKnowledgeBacklogReader{
		err: errors.New("neo4j unavailable"),
	}, logger)

	collector.Collect(make(chan prometheus.Metric, 1))

	if len(logger.warns) != 1 {
		t.Fatalf("warn count = %d; want 1", len(logger.warns))
	}
	if logger.warns[0] != "knowledge backlog metrics collection failed" {
		t.Fatalf("warn message = %q", logger.warns[0])
	}
}

func TestKnowledgeBacklogCollector_ParsesRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  map[string]any
		want knowledgeBacklogSample
	}{
		{
			name: "int64 count",
			row: map[string]any{
				"team_id":    "team-1",
				"profile_id": "profile-1",
				"item":       "claim_candidate",
				"count":      int64(3),
			},
			want: knowledgeBacklogSample{
				TeamID:    "team-1",
				ProfileID: "profile-1",
				Item:      "claim_candidate",
				Count:     3,
			},
		},
		{
			name: "int count",
			row: map[string]any{
				"team_id":    "team-2",
				"profile_id": "profile-2",
				"item":       "claim_validated",
				"count":      2,
			},
			want: knowledgeBacklogSample{
				TeamID:    "team-2",
				ProfileID: "profile-2",
				Item:      "claim_validated",
				Count:     2,
			},
		},
		{
			name: "float64 count and non-string labels",
			row: map[string]any{
				"team_id":    7,
				"profile_id": nil,
				"item":       "claim_disputed",
				"count":      1.0,
			},
			want: knowledgeBacklogSample{
				Item:  "claim_disputed",
				Count: 1,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := knowledgeBacklogSampleFromMap(tc.row)
			if err != nil {
				t.Fatalf("knowledgeBacklogSampleFromMap error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("knowledgeBacklogSampleFromMap = %+v; want %+v", got, tc.want)
			}
		})
	}

	_, err := knowledgeBacklogSampleFromMap(map[string]any{
		"team_id":    "team-1",
		"profile_id": "profile-1",
		"item":       "claim_candidate",
		"count":      "3",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid backlog count") {
		t.Fatalf("invalid count error = %v", err)
	}
}

func TestPrometheusMetrics_RegisterKnowledgeBacklogCollectorIgnoresNilInputs(t *testing.T) {
	var nilMetrics *PrometheusMetrics
	nilMetrics.RegisterKnowledgeBacklogCollector(&fakeKnowledgeBacklogReader{}, nil)

	metrics := NewPrometheusMetrics()
	metrics.RegisterKnowledgeBacklogCollector(nil, nil)
}

func TestNoopDiscoverabilityMetrics_ConsumesCalls(t *testing.T) {
	metrics := NoopDiscoverabilityMetrics()

	metrics.ObserveEmbeddingLatency(1, "ok")
	metrics.IncEmbeddingError("timeout")
	metrics.ObserveRecallLatency(1)
	metrics.ObserveRecall(1, 2, "ok")
	metrics.ObserveMemoryFunnelLatency("claim_to_verify", 1, "verified")
	metrics.IncFragmentCreate("created")
	metrics.IncClaimCreate("duplicate", "exact")
	metrics.IncVerifyVerdict("verified")
	metrics.IncPromotionOutcome("promoted")
	metrics.ObservePromoteLockWait(1)
	metrics.IncFragmentRetract()
	metrics.IncFactNeedsRevalidation()
	metrics.IncCommunityDetect("ok")
	metrics.ObserveCommunityDetect(1, 2)
}

type fakeKnowledgeBacklogReader struct {
	samples   []knowledgeBacklogSample
	result    any
	useResult bool
	err       error
	calls     int
}

func (f *fakeKnowledgeBacklogReader) ExecuteRead(context.Context, neo4j.ManagedTransactionWork) (any, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.useResult {
		return f.result, nil
	}
	return f.samples, nil
}

type fakeLogProvider struct {
	warns []string
}

func (f *fakeLogProvider) Info(string, ...LogAttr) {}

func (f *fakeLogProvider) Error(string, error, ...LogAttr) {}

func (f *fakeLogProvider) Warn(msg string, _ ...LogAttr) {
	f.warns = append(f.warns, msg)
}

func (f *fakeLogProvider) Debug(string, ...LogAttr) {}

func (f *fakeLogProvider) With(...LogAttr) LogProvider { return f }

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
	if got := len(metrics.RecallLatencies()); got != 2 {
		t.Fatalf("fallback recall latencies = %d; want 2", got)
	}
	recalls := metrics.RecallSamples()
	if len(recalls) != 1 || recalls[0].ResultCount != 4 || recalls[0].Outcome != "ok" {
		t.Fatalf("fallback recall samples = %+v", recalls)
	}
	funnel := metrics.MemoryFunnelSamples()
	if len(funnel) != 1 || funnel[0].Stage != "claim_to_verify" || funnel[0].Outcome != "verified" {
		t.Fatalf("fallback funnel samples = %+v", funnel)
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
	RecordRecall(ctx, nil, 1, 1, "ok")
	RecordMemoryFunnelLatency(ctx, nil, "stage", 1, "ok")
	RecordFragmentCreate(ctx, nil, "created")
	RecordClaimCreate(ctx, nil, "created", "")
	RecordPromotionOutcome(ctx, nil, "promoted")
	RecordPromoteLockWait(ctx, nil, 0.1)
	RecordFragmentRetract(ctx, nil)
	RecordFactNeedsRevalidation(ctx, nil)
}
