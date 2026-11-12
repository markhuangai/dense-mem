package observability

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

const operationalLedgerCollectionTimeout = 2 * time.Second

var aiOperationLabels = []string{
	AIOperationSemanticAssessment,

	AIOperationRecallEmbedding,
	AIOperationRecallReranking,
	AIOperationSearchDocumentEmbedding,

	AIOperationOntologyOrganization,
}

type LogicalOperationMetrics interface {
	ObserveLogicalOperation(operation, classification, outcome string, duration time.Duration)
	ObserveLogicalRecovery(operation, outcome string)
	ObserveRememberPhase(phase, outcome string, duration time.Duration)
}

func RecordLogicalOperation(metrics DiscoverabilityMetrics, operation, classification, outcome string, duration time.Duration) {
	if recorder, ok := metrics.(LogicalOperationMetrics); ok {
		recorder.ObserveLogicalOperation(operation, classification, outcome, duration)
	}
}

func RecordLogicalRecovery(metrics DiscoverabilityMetrics, operation, outcome string) {
	if recorder, ok := metrics.(LogicalOperationMetrics); ok {
		recorder.ObserveLogicalRecovery(operation, outcome)
	}
}

func RecordRememberPhase(metrics DiscoverabilityMetrics, phase, outcome string, duration time.Duration) {
	if recorder, ok := metrics.(LogicalOperationMetrics); ok {
		recorder.ObserveRememberPhase(phase, outcome, duration)
	}
}

type operationalPrometheusMetrics struct {
	mcpTransportRequests *prometheus.CounterVec
	mcpTransportDuration *prometheus.HistogramVec
	mcpToolResults       *prometheus.CounterVec
	logicalOperations    *prometheus.CounterVec
	logicalDuration      *prometheus.HistogramVec
	logicalRecoveries    *prometheus.CounterVec
	rememberPhases       *prometheus.HistogramVec
	providerTokens       *prometheus.CounterVec
	providerUnpriced     *prometheus.CounterVec
}

func newOperationalPrometheusMetrics() *operationalPrometheusMetrics {
	metrics := &operationalPrometheusMetrics{
		mcpTransportRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_mcp_transport_requests_total",
			Help: "Authenticated MCP transport attempts classified by method and HTTP result.",
		}, []string{"method", "status_class"}),
		mcpTransportDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_mcp_transport_duration_seconds",
			Help:    "Authenticated MCP transport request duration.",
			Buckets: requestDurationBuckets(),
		}, []string{"method", "status_class"}),
		mcpToolResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_mcp_tool_results_total",
			Help: "MCP logical tool results, independent of their HTTP transport status.",
		}, []string{"outcome"}),
		logicalOperations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_logical_operation_attempts_total",
			Help: "Logical operation attempts by bounded execution, replay, conflict, and result labels.",
		}, []string{"operation", "classification", "outcome"}),
		logicalDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_logical_operation_duration_seconds",
			Help:    "Logical operation duration by bounded outcome.",
			Buckets: requestDurationBuckets(),
		}, []string{"operation", "outcome"}),
		logicalRecoveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_logical_operation_recoveries_total",
			Help: "Remember recovery attempts and their terminal result.",
		}, []string{"operation", "outcome"}),
		rememberPhases: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_remember_phase_duration_seconds",
			Help:    "Remember assessment, embedding, and commit phase durations.",
			Buckets: requestDurationBuckets(),
		}, []string{"phase", "outcome"}),
		providerTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_operation_provider_tokens_total",
			Help: "Observed provider or tokenizer tokens without identity or model labels.",
		}, []string{"operation", "component", "kind", "source"}),
		providerUnpriced: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_operation_provider_usage_unpriced_total",
			Help: "Provider usage that cannot be priced by bounded reason.",
		}, []string{"operation", "component", "reason"}),
	}
	metrics.seedBoundedSeries()
	return metrics
}

func (m *operationalPrometheusMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.mcpTransportRequests, m.mcpTransportDuration, m.mcpToolResults,
		m.logicalOperations, m.logicalDuration, m.logicalRecoveries, m.rememberPhases,

		m.providerTokens, m.providerUnpriced,
	}
}

func (m *operationalPrometheusMetrics) seedBoundedSeries() {
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OTHER"} {
		for _, status := range []string{"1xx", "2xx", "3xx", "4xx", "5xx"} {
			m.mcpTransportRequests.WithLabelValues(method, status)
			m.mcpTransportDuration.WithLabelValues(method, status)
		}
	}
	for _, outcome := range []string{"success", "cancelled", "rpc_error", "tool_error", "missing_result", "other"} {
		m.mcpToolResults.WithLabelValues(outcome)
	}
	for _, operation := range []string{"remember"} {
		for _, classification := range []string{"execution", "replay", "conflict", "confirmation", "feedback", "recovery"} {
			for _, outcome := range logicalOperationOutcomes {
				m.logicalOperations.WithLabelValues(operation, classification, outcome)
			}
		}
		for _, outcome := range logicalOperationOutcomes {
			m.logicalDuration.WithLabelValues(operation, outcome)
		}
	}
	for _, operation := range []string{"remember"} {
		for _, outcome := range []string{"attempted", "succeeded", "failed", "cancelled"} {
			m.logicalRecoveries.WithLabelValues(operation, outcome)
		}
	}
	for _, phase := range []string{"assessment", "embedding", "commit"} {
		for _, outcome := range []string{"ok", "failed", "cancelled"} {
			m.rememberPhases.WithLabelValues(phase, outcome)
		}
	}
	for _, operation := range append(append([]string(nil), aiOperationLabels...), unknownMetricLabel) {
		for _, component := range []string{AIComponentVerifier, AIComponentEmbedding, AIComponentDecision, unknownMetricLabel} {
			for _, kind := range []string{"input", "output", "total"} {
				for _, source := range []string{AITokenSourceProvider, AITokenSourceTokenizer, unknownMetricLabel} {
					m.providerTokens.WithLabelValues(operation, component, kind, source)
				}
			}
			for _, reason := range []string{"missing_usage", "missing_price", "tokenizer_error", "invalid_usage", "pricing_unavailable", unknownMetricLabel} {
				m.providerUnpriced.WithLabelValues(operation, component, reason)
			}
		}
	}
}

var (
	logicalOperationOutcomes = []string{"completed", "failed", "cancelled", "replayed", "evaluated_zero", "conflict", "ok", "error", "skipped"}
)

func requestDurationBuckets() []float64 {
	buckets := append([]float64(nil), prometheus.DefBuckets...)
	return durationBuckets(append(buckets, 30, 60, 120, 160, 170, 180, 240, 300, 600))
}

func durationBuckets(buckets []float64) []float64 {
	values := append([]float64(nil), buckets...)
	values = append(values, 30, 60, 120, 160, 170, 180, 240, 300, 600)
	sort.Float64s(values)
	unique := values[:0]
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func (m *PrometheusMetrics) ObserveMCPTransportRequest(method string, status int, duration time.Duration) {
	if m == nil || m.operational == nil || duration < 0 {
		return
	}
	method = boundedMetricLabel(strings.ToUpper(strings.TrimSpace(method)), []string{"GET", "POST", "PUT", "PATCH", "DELETE"})
	class := statusClass(status)
	m.operational.mcpTransportRequests.WithLabelValues(method, class).Inc()
	m.operational.mcpTransportDuration.WithLabelValues(method, class).Observe(duration.Seconds())
}

func (m *PrometheusMetrics) ObserveMCPToolResult(outcome string, count int64) {
	if m == nil || m.operational == nil || count < 1 {
		return
	}
	m.operational.mcpToolResults.WithLabelValues(boundedMetricLabel(outcome, []string{"success", "cancelled", "rpc_error", "tool_error", "missing_result", "other"})).Add(float64(count))
}

func (m *PrometheusMetrics) ObserveLogicalOperation(operation, classification, outcome string, duration time.Duration) {
	if m == nil || m.operational == nil {
		return
	}
	operation = boundedMetricLabel(operation, []string{"remember"})
	classification = boundedMetricLabel(classification, []string{"execution", "replay", "conflict", "confirmation", "feedback", "recovery"})
	outcome = boundedMetricLabel(outcome, logicalOperationOutcomes)
	m.operational.logicalOperations.WithLabelValues(operation, classification, outcome).Inc()
	if duration >= 0 {
		m.operational.logicalDuration.WithLabelValues(operation, outcome).Observe(duration.Seconds())
	}
}

func (m *PrometheusMetrics) ObserveLogicalRecovery(operation, outcome string) {
	if m == nil || m.operational == nil {
		return
	}
	operation = boundedMetricLabel(operation, []string{"remember"})
	outcome = boundedMetricLabel(outcome, []string{"attempted", "succeeded", "failed", "cancelled"})
	m.operational.logicalRecoveries.WithLabelValues(operation, outcome).Inc()
}

func (m *PrometheusMetrics) ObserveRememberPhase(phase, outcome string, duration time.Duration) {
	if m == nil || m.operational == nil || duration < 0 {
		return
	}
	phase = boundedMetricLabel(phase, []string{"assessment", "embedding", "commit"})
	outcome = boundedMetricLabel(outcome, []string{"ok", "failed", "cancelled"})
	m.operational.rememberPhases.WithLabelValues(phase, outcome).Observe(duration.Seconds())
}

func (m *PrometheusMetrics) observeProviderTokens(operation, component, kind, source string, count int64) {
	if m == nil || m.operational == nil || count <= 0 {
		return
	}
	operation = boundedMetricLabel(operation, append(append([]string(nil), aiOperationLabels...), unknownMetricLabel))
	component = boundedMetricLabel(component, []string{AIComponentVerifier, AIComponentEmbedding, AIComponentDecision})
	kind = boundedMetricLabel(kind, []string{"input", "output", "total"})
	source = boundedMetricLabel(source, []string{AITokenSourceProvider, AITokenSourceTokenizer})
	m.operational.providerTokens.WithLabelValues(operation, component, kind, source).Add(float64(count))
}

func (m *PrometheusMetrics) observeProviderUnpriced(operation, component, reason string) {
	if m == nil || m.operational == nil {
		return
	}
	operation = boundedMetricLabel(operation, append(append([]string(nil), aiOperationLabels...), unknownMetricLabel))
	component = boundedMetricLabel(component, []string{AIComponentVerifier, AIComponentEmbedding, AIComponentDecision})
	reason = boundedMetricLabel(reason, []string{"missing_usage", "missing_price", "tokenizer_error", "invalid_usage", "pricing_unavailable"})
	m.operational.providerUnpriced.WithLabelValues(operation, component, reason).Inc()
}

func boundedMetricLabel(value string, allowed []string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return unknownMetricLabel
}

func (m *PrometheusMetrics) RegisterOperationalTelemetryCollector(reader operationscontract.OperationalTelemetryReader) error {
	if m == nil || m.registry == nil {
		return nil
	}
	return m.registry.Register(NewOperationalTelemetryCollector(reader))
}

type OperationalTelemetryCollector struct {
	reader           operationscontract.OperationalTelemetryReader
	collection       singleflight.Group
	status           *prometheus.Desc
	rememberAttempts *prometheus.Desc
}

func NewOperationalTelemetryCollector(reader operationscontract.OperationalTelemetryReader) *OperationalTelemetryCollector {
	return &OperationalTelemetryCollector{reader: reader,
		status:           prometheus.NewDesc("densemem_operational_ledger_collection_success", "Whether the latest canonical-ledger collection succeeded.", nil, nil),
		rememberAttempts: prometheus.NewDesc("densemem_operational_remember_attempts", "Canonical Remember attempts by durable outcome and window.", []string{"window", "outcome"}, nil)}
}
func (c *OperationalTelemetryCollector) descriptors() []*prometheus.Desc {
	return []*prometheus.Desc{c.status, c.rememberAttempts}
}
func (c *OperationalTelemetryCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.descriptors() {
		ch <- desc
	}
}

func (c *OperationalTelemetryCollector) readOperationalTelemetry() <-chan singleflight.Result {
	return c.collection.DoChan("canonical-ledger", func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), operationalLedgerCollectionTimeout)
		defer cancel()
		return c.reader.ReadOperationalTelemetry(ctx)
	})
}

func (c *OperationalTelemetryCollector) Collect(ch chan<- prometheus.Metric) {
	if c == nil {
		return
	}
	if c.reader == nil {
		ch <- prometheus.MustNewConstMetric(c.status, prometheus.GaugeValue, 0)
		return
	}
	result := <-c.readOperationalTelemetry()
	if result.Err != nil {
		ch <- prometheus.MustNewConstMetric(c.status, prometheus.GaugeValue, 0)
		return
	}
	snapshot := result.Val.(operationscontract.OperationalTelemetrySnapshot)
	c.collectSnapshot(ch, snapshot)
	ch <- prometheus.MustNewConstMetric(c.status, prometheus.GaugeValue, 1)
}

func (c *OperationalTelemetryCollector) collectSnapshot(ch chan<- prometheus.Metric, snapshot operationscontract.OperationalTelemetrySnapshot) {
	c.collectWindowed(ch, snapshot.RememberAttempts, c.rememberAttempts, []string{"completed", "rejected", "quarantined", "failed", "replayed"})
}

func (c *OperationalTelemetryCollector) collectWindowed(ch chan<- prometheus.Metric, values []operationscontract.WindowedTelemetryCount, desc *prometheus.Desc, kinds []string) {
	counts := make(map[string]float64, len(values))
	for _, value := range values {
		window := boundedMetricLabel(value.Window, telemetryMetricWindows)
		kind := boundedMetricLabel(value.Kind, kinds)
		counts[window+"/"+kind] += value.Count
	}
	for _, window := range telemetryMetricWindows {
		for _, kind := range kinds {
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, counts[window+"/"+kind], window, kind)
		}
	}
}

var telemetryMetricWindows = []string{"15m", "30m", "1h", "12h", "1d", "7d", "30d"}
