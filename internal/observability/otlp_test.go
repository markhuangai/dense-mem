package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	prombridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	metricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestOTLPAggregatesCountersAndHistogramBucketsAcrossIdentity(t *testing.T) {
	registry := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "densemem_recall_requests_total"}, []string{"team_id", "profile_id", "credential_id", "outcome"})
	histogram := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "densemem_recall_duration_seconds", Buckets: []float64{1, 2, 5}}, []string{"team_id", "profile_id"})
	registry.MustRegister(counter, histogram)
	counter.WithLabelValues("team-a", "owner-a", "credential-secret-a", "success").(prometheus.ExemplarAdder).AddWithExemplar(2, prometheus.Labels{"content": "exemplar-content-canary"})
	counter.WithLabelValues("team-b", "owner-b", "credential-secret-b", "success").Add(3)
	histogram.WithLabelValues("team-a", "owner-a").(prometheus.ExemplarObserver).ObserveWithExemplar(0.5, prometheus.Labels{"content": "exemplar-content-canary"})
	histogram.WithLabelValues("team-b", "owner-b").Observe(3)
	producer := prombridge.NewMetricProducer(prombridge.WithGatherer(exportGatherer{source: registry}))
	metrics, err := producer.Produce(context.Background())
	require.NoError(t, err)
	for _, metric := range metrics[0].Metrics {
		switch data := metric.Data.(type) {
		case metricdata.Sum[float64]:
			require.Len(t, data.DataPoints, 1)
			require.Equal(t, float64(5), data.DataPoints[0].Value)
			require.Equal(t, 1, data.DataPoints[0].Attributes.Len())
		case metricdata.Histogram[float64]:
			require.Len(t, data.DataPoints, 1)
			point := data.DataPoints[0]
			require.Equal(t, uint64(2), point.Count)
			require.Equal(t, 3.5, point.Sum)
			require.Equal(t, []uint64{1, 0, 1, 0}, point.BucketCounts)
			require.Zero(t, point.Attributes.Len())
			require.Empty(t, point.Exemplars)
		default:
			t.Fatalf("unexpected metric type %T", data)
		}
	}
	raw, err := json.Marshal(metrics)
	require.NoError(t, err)
	for _, canary := range []string{"team-a", "owner-b", "credential-secret", "exemplar-content-canary"} {
		require.NotContains(t, string(raw), canary)
	}
}

func TestOTLPPreservesProductionSuccessOutcomes(t *testing.T) {
	metrics := NewPrometheusMetrics()
	metrics.ObserveRecallLatencyFor(context.Background(), 1)
	metrics.ObserveRecallFor(context.Background(), 1, 1, "outcome-content-canary")
	metrics.ObserveRememberAcknowledgement(context.Background(), 1, "ok")
	families, err := (exportGatherer{source: metrics.registry}).Gather()
	require.NoError(t, err)
	observed := make(map[string]map[string]float64)
	for _, family := range families {
		if family.GetName() != "densemem_recall_requests_total" && family.GetName() != "densemem_remember_acknowledgements_total" {
			continue
		}
		outcomes := make(map[string]float64)
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "outcome" {
					outcomes[label.GetValue()] = metric.GetCounter().GetValue()
				}
			}
		}
		observed[family.GetName()] = outcomes
	}
	require.Equal(t, map[string]float64{"ok": 1, "other": 1}, observed["densemem_recall_requests_total"])
	require.Equal(t, map[string]float64{"ok": 1}, observed["densemem_remember_acknowledgements_total"])
}

func TestOTLPHTTPProtobufUsesOnlyApprovedContentAndSeparateHeaders(t *testing.T) {
	var mu sync.Mutex
	var traces []*tracev1.ExportTraceServiceRequest
	var metrics []*metricsv1.ExportMetricsServiceRequest
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/traces":
			if r.Header.Get("Authorization") != "Bearer trace-secret" {
				w.WriteHeader(401)
				return
			}
			value := new(tracev1.ExportTraceServiceRequest)
			if proto.Unmarshal(raw, value) != nil {
				w.WriteHeader(400)
				return
			}
			traces = append(traces, value)
		case "/v1/metrics":
			if r.Header.Get("Authorization") != "Bearer metric-secret" {
				w.WriteHeader(401)
				return
			}
			value := new(metricsv1.ExportMetricsServiceRequest)
			if proto.Unmarshal(raw, value) != nil {
				w.WriteHeader(400)
				return
			}
			metrics = append(metrics, value)
		default:
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "content=environment-content-canary")
	recorder := NewPrometheusMetrics()
	recorder.ObserveRecall(5, 2, "success")
	recorder.ObserveVerifierLatencyFor(context.Background(), "trace-secret", 1, "outcome-content-canary")
	recorder.ObserveEmbeddingLatencyFor(context.Background(), "safe-model", 1, "success")
	exports, err := NewOTLP(context.Background(), recorder, OTLPOptions{TraceEndpoint: collector.URL + "/v1/traces", MetricEndpoint: collector.URL + "/v1/metrics", TraceHeaders: map[string]string{"Authorization": "Bearer trace-secret"}, MetricHeaders: map[string]string{"Authorization": "Bearer metric-secret"}, Models: []string{"safe-model", "trace-secret"}, Protector: NewCredentialProtector("trace-secret", "metric-secret")})
	require.NoError(t, err)
	defer exports.Shutdown(context.Background())
	member, err := baggage.NewMember("content", "baggage-content-canary")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	exports.ObserveOperation(baggage.ContextWithBaggage(context.Background(), bag), "remember", time.Now().Add(-time.Millisecond), "success")
	exports.ObserveOperation(context.Background(), "untrusted-tool-content-canary", time.Now(), "exception-content-canary")
	require.NoError(t, exports.ForceFlush(context.Background()))
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, traces)
	require.NotEmpty(t, metrics)
	for _, values := range [][]proto.Message{{traces[0]}, {metrics[0]}} {
		for _, value := range values {
			raw := protojson.Format(value)
			for _, canary := range []string{"trace-secret", "metric-secret", "content-canary", "untrusted-tool", "exception-content"} {
				require.NotContains(t, raw, canary)
			}
			require.Contains(t, raw, "dense-mem")
		}
	}
	require.Equal(t, "mcp.remember", traces[0].ResourceSpans[0].ScopeSpans[0].Spans[0].Name)
	require.False(t, exports.Health().Traces.Degraded)
	require.False(t, exports.Health().Metrics.Degraded)
}

func TestOTLPSlowAndUnavailableCollectorKeepsAdmissionBounded(t *testing.T) {
	release := make(chan struct{})
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	exports, err := NewOTLP(context.Background(), NewPrometheusMetrics(), OTLPOptions{TraceEndpoint: collector.URL + "/v1/traces"})
	require.NoError(t, err)
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	defer collector.Close()
	defer exports.Shutdown(context.Background())
	defer closeRelease()
	started := time.Now()
	for i := 0; i < 10000; i++ {
		exports.ObserveOperation(context.Background(), "recall", time.Now(), "success")
	}
	require.Less(t, time.Since(started), 2*time.Second)
	health := exports.Health()
	require.LessOrEqual(t, health.PendingSpans, int64(2048))
	require.Positive(t, health.DroppedSpans)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.Error(t, exports.ForceFlush(ctx))
	closeRelease()
	require.NoError(t, exports.ForceFlush(context.Background()))
	require.Positive(t, exports.Health().Traces.Failures)
	require.True(t, exports.Health().Traces.Degraded)
}

func TestOTLPCollectionFailureReportsHealthAndLogsOnlyTransitions(t *testing.T) {
	var output bytes.Buffer
	health := exportHealthState{enabled: true, kind: "metrics", logger: NewConsoleWithHandler(slog.NewJSONHandler(&output, nil))}
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "densemem_recall_requests_total"})
	registry.MustRegister(gauge)
	gatherer := exportGatherer{source: registry, health: &health}
	for i := 0; i < 2; i++ {
		_, err := gatherer.Gather()
		require.ErrorContains(t, err, "metric type is unsupported")
	}
	require.True(t, health.snapshot().Degraded)
	require.Equal(t, uint64(2), health.snapshot().CollectionFailures)
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("optional OTLP export degraded")))
	require.True(t, registry.Unregister(gauge))
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "densemem_recall_requests_total"})
	registry.MustRegister(counter)
	_, err := gatherer.Gather()
	require.NoError(t, err)
	require.False(t, health.snapshot().Degraded)
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("optional OTLP export recovered")))
	gatherer.source = prometheus.Gatherers{registry, registry}
	_, err = gatherer.Gather()
	require.ErrorContains(t, err, "metric collection failed")
	require.True(t, health.snapshot().Degraded)
	require.Equal(t, uint64(3), health.snapshot().CollectionFailures)
	require.NotContains(t, output.String(), "collected before")
}

func TestOTLPRejectsHealthRegistrationCollision(t *testing.T) {
	metrics := NewPrometheusMetrics()
	metrics.registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "densemem_otlp_pending_spans"}))
	exports, err := NewOTLP(context.Background(), metrics, OTLPOptions{})
	require.Nil(t, exports)
	require.ErrorContains(t, err, "local health registration failed")
}
