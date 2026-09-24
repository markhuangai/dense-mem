package observability

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"

	"github.com/markhuangai/dense-mem/internal/domain"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

const operationalLedgerCollectionTimeout = 2 * time.Second

var aiOperationLabels = []string{
	AIOperationSemanticAssessment,
	AIOperationConflictReview,
	AIOperationDreamGeneration,
	AIOperationEvidenceDiscovery,
	AIOperationRecallEmbedding,
	AIOperationSearchDocumentEmbedding,
	AIOperationCommunitySummary,
}

type LogicalOperationMetrics interface {
	ObserveLogicalOperation(operation, classification, outcome string, duration time.Duration)
	ObserveLogicalRecovery(operation, outcome string)
	ObserveRememberPhase(phase, outcome string, duration time.Duration)
	ObserveDreamCycle(lane, status string, duration time.Duration)
	ObserveDreamProviderAttempt(stage, outcome string, duration time.Duration)
	ObserveRecallHypotheses(outcome string, returned int)
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

func RecordDreamCycle(metrics DiscoverabilityMetrics, lane, status string, duration time.Duration) {
	if recorder, ok := metrics.(LogicalOperationMetrics); ok {
		recorder.ObserveDreamCycle(lane, status, duration)
	}
}

func RecordDreamProviderAttempt(metrics DiscoverabilityMetrics, stage, outcome string, duration time.Duration) {
	if recorder, ok := metrics.(LogicalOperationMetrics); ok {
		recorder.ObserveDreamProviderAttempt(stage, outcome, duration)
	}
}

func RecordRecallHypotheses(metrics DiscoverabilityMetrics, outcome string, returned int) {
	if recorder, ok := metrics.(LogicalOperationMetrics); ok {
		recorder.ObserveRecallHypotheses(outcome, returned)
	}
}

type operationalPrometheusMetrics struct {
	mcpTransportRequests       *prometheus.CounterVec
	mcpTransportDuration       *prometheus.HistogramVec
	mcpToolResults             *prometheus.CounterVec
	logicalOperations          *prometheus.CounterVec
	logicalDuration            *prometheus.HistogramVec
	logicalRecoveries          *prometheus.CounterVec
	rememberPhases             *prometheus.HistogramVec
	dreamCycles                *prometheus.CounterVec
	dreamCycleDuration         *prometheus.HistogramVec
	dreamProviderCalls         *prometheus.CounterVec
	dreamProviderDuration      *prometheus.HistogramVec
	dreamFeedbackActions       *prometheus.CounterVec
	recallHypothesisExpansions *prometheus.CounterVec
	recallHypothesesReturned   prometheus.Counter
	providerTokens             *prometheus.CounterVec
	providerUnpriced           *prometheus.CounterVec
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
			Help: "Remember and Dream recovery attempts and their terminal result.",
		}, []string{"operation", "outcome"}),
		rememberPhases: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_remember_phase_duration_seconds",
			Help:    "Remember assessment, embedding, and commit phase durations.",
			Buckets: requestDurationBuckets(),
		}, []string{"phase", "outcome"}),
		dreamCycles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_dream_cycle_attempts_total",
			Help: "Dream cycle attempts by generator lane and terminal result.",
		}, []string{"lane", "status"}),
		dreamCycleDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_dream_cycle_duration_seconds",
			Help:    "Dream cycle duration by generator lane and terminal result.",
			Buckets: requestDurationBuckets(),
		}, []string{"lane", "status"}),
		dreamProviderCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_dream_provider_attempts_total",
			Help: "Dream provider generation attempts by bounded stage and outcome.",
		}, []string{"stage", "outcome"}),
		dreamProviderDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "densemem_dream_provider_duration_seconds",
			Help:    "Dream provider generation duration by bounded stage and outcome.",
			Buckets: requestDurationBuckets(),
		}, []string{"stage", "outcome"}),
		dreamFeedbackActions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_dream_feedback_actions_total",
			Help: "Explicit Dream feedback actions, including ignore requests, by result.",
		}, []string{"decision", "outcome"}),
		recallHypothesisExpansions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "densemem_recall_hypothesis_expansions_total",
			Help: "Recall hypothesis expansion results, including empty and unavailable results.",
		}, []string{"outcome"}),
		recallHypothesesReturned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "densemem_recall_hypotheses_returned_total",
			Help: "Related Hypothesis summaries returned in final Recall results.",
		}),
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
		m.dreamCycles, m.dreamCycleDuration, m.dreamProviderCalls, m.dreamProviderDuration,
		m.dreamFeedbackActions, m.recallHypothesisExpansions, m.recallHypothesesReturned,
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
	for _, operation := range []string{"remember", "dream_confirmation", "dream_feedback"} {
		for _, classification := range []string{"execution", "replay", "conflict", "confirmation", "feedback", "recovery"} {
			for _, outcome := range logicalOperationOutcomes {
				m.logicalOperations.WithLabelValues(operation, classification, outcome)
			}
		}
		for _, outcome := range logicalOperationOutcomes {
			m.logicalDuration.WithLabelValues(operation, outcome)
		}
	}
	for _, operation := range []string{"remember", "dream_graph", "dream_evidence"} {
		for _, outcome := range []string{"attempted", "succeeded", "failed", "cancelled"} {
			m.logicalRecoveries.WithLabelValues(operation, outcome)
		}
	}
	for _, phase := range []string{"assessment", "embedding", "commit"} {
		for _, outcome := range []string{"ok", "failed", "cancelled"} {
			m.rememberPhases.WithLabelValues(phase, outcome)
		}
	}
	for _, lane := range []string{"graph", "evidence_discovery"} {
		for _, status := range dreamRunStatuses {
			m.dreamCycles.WithLabelValues(lane, status)
			m.dreamCycleDuration.WithLabelValues(lane, status)
		}
	}
	for _, stage := range []string{"graph_generation", "evidence_discovery"} {
		for _, outcome := range []string{"ok", "error"} {
			m.dreamProviderCalls.WithLabelValues(stage, outcome)
			m.dreamProviderDuration.WithLabelValues(stage, outcome)
		}
	}
	for _, decision := range dreamFeedbackDecisions {
		for _, outcome := range []string{"ok", "error"} {
			m.dreamFeedbackActions.WithLabelValues(decision, outcome)
		}
	}
	for _, outcome := range recallHypothesisOutcomes {
		m.recallHypothesisExpansions.WithLabelValues(outcome)
	}
	for _, operation := range append(append([]string(nil), aiOperationLabels...), unknownMetricLabel) {
		for _, component := range []string{AIComponentVerifier, AIComponentEmbedding, unknownMetricLabel} {
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
	dreamRunStatuses         = []string{"running", "completed", "failed", "skipped", "cancelled", "missed"}
	dreamFeedbackDecisions   = []string{"ignore", "reject", "stale", "reinforce", "confirm_true", "confirm_false", "promote_candidate", unknownMetricLabel}
	recallHypothesisOutcomes = []string{"returned", "empty", "unavailable", "temporal_unsupported", "not_requested"}
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
	operation = boundedMetricLabel(operation, []string{"remember", "dream_confirmation", "dream_feedback"})
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
	operation = boundedMetricLabel(operation, []string{"remember", "dream_graph", "dream_evidence"})
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

func (m *PrometheusMetrics) ObserveDreamCycle(lane, status string, duration time.Duration) {
	if m == nil || m.operational == nil {
		return
	}
	lane = boundedMetricLabel(lane, []string{"graph", "evidence_discovery"})
	status = boundedMetricLabel(status, dreamRunStatuses)
	m.operational.dreamCycles.WithLabelValues(lane, status).Inc()
	if duration >= 0 {
		m.operational.dreamCycleDuration.WithLabelValues(lane, status).Observe(duration.Seconds())
	}
}

func (m *PrometheusMetrics) ObserveDreamProviderAttempt(stage, outcome string, duration time.Duration) {
	if m == nil || m.operational == nil {
		return
	}
	stage = boundedMetricLabel(stage, []string{"graph_generation", "evidence_discovery"})
	outcome = boundedMetricLabel(outcome, []string{"ok", "error"})
	m.operational.dreamProviderCalls.WithLabelValues(stage, outcome).Inc()
	if duration >= 0 {
		m.operational.dreamProviderDuration.WithLabelValues(stage, outcome).Observe(duration.Seconds())
	}
}

func (m *PrometheusMetrics) ObserveRecallHypotheses(outcome string, returned int) {
	if m == nil || m.operational == nil {
		return
	}
	outcome = boundedMetricLabel(outcome, recallHypothesisOutcomes)
	m.operational.recallHypothesisExpansions.WithLabelValues(outcome).Inc()
	if returned > 0 {
		m.operational.recallHypothesesReturned.Add(float64(returned))
	}
}

func (m *PrometheusMetrics) observeDreamFeedbackAction(decision, outcome string) {
	if m == nil || m.operational == nil {
		return
	}
	decision = boundedMetricLabel(decision, dreamFeedbackDecisions[:len(dreamFeedbackDecisions)-1])
	outcome = boundedMetricLabel(outcome, []string{"ok", "error"})
	m.operational.dreamFeedbackActions.WithLabelValues(decision, outcome).Inc()
}

func (m *PrometheusMetrics) observeProviderTokens(operation, component, kind, source string, count int64) {
	if m == nil || m.operational == nil || count <= 0 {
		return
	}
	operation = boundedMetricLabel(operation, append(append([]string(nil), aiOperationLabels...), unknownMetricLabel))
	component = boundedMetricLabel(component, []string{AIComponentVerifier, AIComponentEmbedding})
	kind = boundedMetricLabel(kind, []string{"input", "output", "total"})
	source = boundedMetricLabel(source, []string{AITokenSourceProvider, AITokenSourceTokenizer})
	m.operational.providerTokens.WithLabelValues(operation, component, kind, source).Add(float64(count))
}

func (m *PrometheusMetrics) observeProviderUnpriced(operation, component, reason string) {
	if m == nil || m.operational == nil {
		return
	}
	operation = boundedMetricLabel(operation, append(append([]string(nil), aiOperationLabels...), unknownMetricLabel))
	component = boundedMetricLabel(component, []string{AIComponentVerifier, AIComponentEmbedding})
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
	reader                 operationscontract.OperationalTelemetryReader
	collection             singleflight.Group
	status                 *prometheus.Desc
	runs                   *prometheus.Desc
	inputTargets           *prometheus.Desc
	evidenceTargets        *prometheus.Desc
	evaluatedTargets       *prometheus.Desc
	proposals              *prometheus.Desc
	createdHypotheses      *prometheus.Desc
	rejectedHypotheses     *prometheus.Desc
	hypotheses             *prometheus.Desc
	backlog                *prometheus.Desc
	oldestBacklogAge       *prometheus.Desc
	feedback               *prometheus.Desc
	rememberAttempts       *prometheus.Desc
	confirmedRelationships *prometheus.Desc
	transitions            *prometheus.Desc
	corrections            *prometheus.Desc
	relationshipsCurrent   *prometheus.Desc
}

func NewOperationalTelemetryCollector(reader operationscontract.OperationalTelemetryReader) *OperationalTelemetryCollector {
	const namespace = "densemem_operational_"
	return &OperationalTelemetryCollector{
		reader:                 reader,
		status:                 prometheus.NewDesc(namespace+"ledger_collection_success", "Whether the latest canonical-ledger collection succeeded.", nil, nil),
		runs:                   prometheus.NewDesc(namespace+"dream_runs", "Canonical Dream runs by lane, status, and window.", []string{"window", "lane", "status"}, nil),
		inputTargets:           prometheus.NewDesc(namespace+"dream_input_targets", "Graph relationships considered as Dream inputs by lane, status, and window.", []string{"window", "lane", "status"}, nil),
		evidenceTargets:        prometheus.NewDesc(namespace+"dream_evidence_targets", "Dream evidence-discovery targets by lane, status, and window.", []string{"window", "lane", "status"}, nil),
		evaluatedTargets:       prometheus.NewDesc(namespace+"dream_evaluated_targets", "Evaluated Dream evidence-discovery targets by lane, status, and window.", []string{"window", "lane", "status"}, nil),
		proposals:              prometheus.NewDesc(namespace+"dream_provider_proposals", "Dream proposals returned by providers by lane, status, and window.", []string{"window", "lane", "status"}, nil),
		createdHypotheses:      prometheus.NewDesc(namespace+"dream_created_hypotheses", "Hypotheses created by Dream runs by lane, status, and window.", []string{"window", "lane", "status"}, nil),
		rejectedHypotheses:     prometheus.NewDesc(namespace+"dream_rejected_hypotheses", "Hypotheses rejected by Dream policy by lane, status, and window.", []string{"window", "lane", "status"}, nil),
		hypotheses:             prometheus.NewDesc(namespace+"hypotheses", "Current Hypotheses by origin lane and lifecycle status.", []string{"lane", "status"}, nil),
		backlog:                prometheus.NewDesc(namespace+"hypothesis_backlog", "Current proposed and reinforced Hypotheses by origin lane.", []string{"lane"}, nil),
		oldestBacklogAge:       prometheus.NewDesc(namespace+"hypothesis_oldest_backlog_age_seconds", "Age of the oldest proposed or reinforced Hypothesis by origin lane.", []string{"lane"}, nil),
		feedback:               prometheus.NewDesc(namespace+"dream_feedback_events", "Durable Dream feedback events by decision and window.", []string{"window", "decision"}, nil),
		rememberAttempts:       prometheus.NewDesc(namespace+"remember_attempts", "Canonical Remember attempts by durable outcome and window.", []string{"window", "outcome"}, nil),
		confirmedRelationships: prometheus.NewDesc(namespace+"dream_confirmed_relationships", "Distinct Relationships observed in completed Remember ingests submitted by Dream confirmation by current status and window.", []string{"window", "status"}, nil),
		transitions:            prometheus.NewDesc(namespace+"relationship_transitions", "Durable Relationship lifecycle transitions by status and window.", []string{"window", "status"}, nil),
		corrections:            prometheus.NewDesc(namespace+"relationship_corrections", "Durable Relationship corrections by window.", []string{"window"}, nil),
		relationshipsCurrent:   prometheus.NewDesc(namespace+"relationships_current", "Current canonical Relationships by lifecycle status.", []string{"status"}, nil),
	}
}

func (c *OperationalTelemetryCollector) descriptors() []*prometheus.Desc {
	return []*prometheus.Desc{c.status, c.runs, c.inputTargets, c.evidenceTargets, c.evaluatedTargets,
		c.proposals, c.createdHypotheses, c.rejectedHypotheses, c.hypotheses, c.backlog, c.oldestBacklogAge,
		c.feedback, c.rememberAttempts, c.confirmedRelationships, c.transitions, c.corrections, c.relationshipsCurrent}
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
	runs := make(map[string]operationscontract.DreamRunTelemetry, len(snapshot.DreamRuns))
	for _, run := range snapshot.DreamRuns {
		key := boundedMetricLabel(run.Window, telemetryMetricWindows) + "/" + boundedMetricLabel(run.Lane, []string{"graph", "evidence_discovery"}) + "/" + boundedMetricLabel(run.Status, dreamRunStatuses)
		value := runs[key]
		value.Window = run.Window
		value.Lane = run.Lane
		value.Status = run.Status
		value.Runs += run.Runs
		value.InputTargets += run.InputTargets
		value.EvidenceTargets += run.EvidenceTargets
		value.EvaluatedTargets += run.EvaluatedTargets
		value.ProviderProposals += run.ProviderProposals
		value.CreatedHypotheses += run.CreatedHypotheses
		value.RejectedHypotheses += run.RejectedHypotheses
		runs[key] = value
	}
	for _, window := range telemetryMetricWindows {
		for _, lane := range []string{"graph", "evidence_discovery"} {
			for _, status := range dreamRunStatuses {
				run := runs[window+"/"+lane+"/"+status]
				labels := []string{window, lane, status}
				ch <- prometheus.MustNewConstMetric(c.runs, prometheus.GaugeValue, run.Runs, labels...)
				ch <- prometheus.MustNewConstMetric(c.inputTargets, prometheus.GaugeValue, run.InputTargets, labels...)
				ch <- prometheus.MustNewConstMetric(c.evidenceTargets, prometheus.GaugeValue, run.EvidenceTargets, labels...)
				ch <- prometheus.MustNewConstMetric(c.evaluatedTargets, prometheus.GaugeValue, run.EvaluatedTargets, labels...)
				ch <- prometheus.MustNewConstMetric(c.proposals, prometheus.GaugeValue, run.ProviderProposals, labels...)
				ch <- prometheus.MustNewConstMetric(c.createdHypotheses, prometheus.GaugeValue, run.CreatedHypotheses, labels...)
				ch <- prometheus.MustNewConstMetric(c.rejectedHypotheses, prometheus.GaugeValue, run.RejectedHypotheses, labels...)
			}
		}
	}
	lanes := []string{"graph", "evidence_discovery"}
	hypothesisStatuses := []string{"proposed", "reinforced", "stale", "rejected", "submitted"}
	hypotheses := make(map[string]operationscontract.HypothesisTelemetry, len(snapshot.Hypotheses))
	for _, hypothesis := range snapshot.Hypotheses {
		lane := boundedMetricLabel(hypothesis.Lane, lanes)
		status := boundedMetricLabel(hypothesis.Status, hypothesisStatuses)
		key := lane + "/" + status
		value := hypotheses[key]
		value.Lane = lane
		value.Status = status
		value.Count += hypothesis.Count
		value.BacklogCount += hypothesis.BacklogCount
		if hypothesis.OldestBacklogAge > value.OldestBacklogAge {
			value.OldestBacklogAge = hypothesis.OldestBacklogAge
		}
		hypotheses[key] = value
	}
	for _, lane := range lanes {
		var backlog float64
		var oldest float64
		for _, status := range hypothesisStatuses {
			hypothesis := hypotheses[lane+"/"+status]
			ch <- prometheus.MustNewConstMetric(c.hypotheses, prometheus.GaugeValue, hypothesis.Count, lane, status)
			backlog += hypothesis.BacklogCount
			if hypothesis.OldestBacklogAge > oldest {
				oldest = hypothesis.OldestBacklogAge
			}
		}
		ch <- prometheus.MustNewConstMetric(c.backlog, prometheus.GaugeValue, backlog, lane)
		ch <- prometheus.MustNewConstMetric(c.oldestBacklogAge, prometheus.GaugeValue, oldest, lane)
	}
	c.collectWindowed(ch, snapshot.Feedback, c.feedback, []string{"ignore", "reject", "stale", "reinforce", "confirm_true", "confirm_false", "promote_candidate"})
	c.collectWindowed(ch, snapshot.RememberAttempts, c.rememberAttempts, []string{"completed", "rejected", "quarantined", "failed", "replayed"})
	c.collectWindowed(ch, snapshot.ConfirmedRelationships, c.confirmedRelationships, domain.RelationshipStatuses())
	c.collectWindowed(ch, snapshot.RelationshipTransitions, c.transitions, domain.RelationshipStatuses())
	c.collectWindowed(ch, snapshot.RelationshipCorrections, c.corrections, []string{"corrections"})
	current := make(map[string]float64, len(snapshot.RelationshipsCurrent))
	for _, value := range snapshot.RelationshipsCurrent {
		status := boundedMetricLabel(value.Kind, domain.RelationshipStatuses())
		current[status] += value.Count
	}
	for _, status := range domain.RelationshipStatuses() {
		ch <- prometheus.MustNewConstMetric(c.relationshipsCurrent, prometheus.GaugeValue, current[status], status)
	}
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
			if desc == c.corrections {
				ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, counts[window+"/"+kind], window)
			} else {
				ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, counts[window+"/"+kind], window, kind)
			}
		}
	}
}

var telemetryMetricWindows = []string{"15m", "30m", "1h", "12h", "1d", "7d", "30d"}
