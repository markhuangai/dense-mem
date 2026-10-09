package observability

import (
	"context"
	"errors"
	"github.com/prometheus/client_golang/prometheus"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	prombridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const OTLPSpanQueueSize = 2048
const OTLPBatchSize = 512
const OTLPDeadline = 5 * time.Second
const OTLPMetricInterval = 60 * time.Second

type OTLPOptions struct {
	TraceEndpoint  string
	MetricEndpoint string
	TraceHeaders   map[string]string
	MetricHeaders  map[string]string
	Models         []string
	Protector      *CredentialProtector
	Logger         LogProvider
}

type ExportDestinationHealth struct {
	Enabled            bool   `json:"enabled"`
	Degraded           bool   `json:"degraded"`
	Batches            uint64 `json:"batches"`
	Failures           uint64 `json:"failures"`
	CollectionFailures uint64 `json:"collection_failures"`
}

type ExportHealth struct {
	Traces        ExportDestinationHealth `json:"traces"`
	Metrics       ExportDestinationHealth `json:"metrics"`
	PendingSpans  int64                   `json:"pending_spans"`
	DroppedSpans  uint64                  `json:"dropped_spans"`
	QueueCapacity int                     `json:"queue_capacity"`
}

type exportHealthState struct {
	enabled            bool
	degraded           atomic.Bool
	batches            atomic.Uint64
	failures           atomic.Uint64
	collectionDegraded atomic.Bool
	collectionFailures atomic.Uint64
	loggedDegraded     atomic.Bool
	logger             LogProvider
	kind               string
}

func (h *exportHealthState) record(err error) {
	failed := err != nil
	if failed {
		h.failures.Add(1)
	} else {
		h.batches.Add(1)
	}
	h.degraded.Store(failed)
	h.logTransition()
}

func (h *exportHealthState) recordCollection(err error) {
	if err != nil {
		h.collectionFailures.Add(1)
	}
	h.collectionDegraded.Store(err != nil)
	h.logTransition()
}

func (h *exportHealthState) logTransition() {
	failed := h.degraded.Load() || h.collectionDegraded.Load()
	if h.loggedDegraded.Swap(failed) == failed || h.logger == nil {
		return
	}
	if failed {
		h.logger.Warn("optional OTLP export degraded", String("destination_kind", h.kind), String("reason", "export_failed"))
	} else {
		h.logger.Info("optional OTLP export recovered", String("destination_kind", h.kind))
	}
}

func (h *exportHealthState) snapshot() ExportDestinationHealth {
	return ExportDestinationHealth{Enabled: h.enabled, Degraded: h.degraded.Load() || h.collectionDegraded.Load(), Batches: h.batches.Load(), Failures: h.failures.Load(), CollectionFailures: h.collectionFailures.Load()}
}

type OTLP struct {
	traces       *sdktrace.TracerProvider
	metrics      *sdkmetric.MeterProvider
	tracer       trace.Tracer
	traceHealth  exportHealthState
	metricHealth exportHealthState
	pending      atomic.Int64
	dropped      atomic.Uint64
	mu           sync.RWMutex
	closed       bool
}

func exportHTTPClient() *http.Client {
	return &http.Client{Timeout: OTLPDeadline, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OTLP redirects are disabled") }}
}

func NewOTLP(ctx context.Context, metrics *PrometheusMetrics, options OTLPOptions) (*OTLP, error) {
	if metrics == nil {
		return nil, errors.New("OTLP requires telemetry collection")
	}
	o := &OTLP{traceHealth: exportHealthState{enabled: options.TraceEndpoint != "", logger: options.Logger, kind: "traces"}, metricHealth: exportHealthState{enabled: options.MetricEndpoint != "", logger: options.Logger, kind: "metrics"}}
	res := resource.NewSchemaless(attribute.String("service.name", "dense-mem"))
	if options.TraceEndpoint != "" {
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(options.TraceEndpoint), otlptracehttp.WithHeaders(options.TraceHeaders), otlptracehttp.WithTimeout(OTLPDeadline), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}), otlptracehttp.WithHTTPClient(exportHTTPClient()), otlptracehttp.WithEncoding(otlptracehttp.EncodingProtobuf))
		if err != nil {
			return nil, errors.New("OTLP trace initialization failed")
		}
		processor := sdktrace.NewBatchSpanProcessor(&localTraceExporter{SpanExporter: exporter, owner: o, resource: res}, sdktrace.WithMaxQueueSize(OTLPSpanQueueSize), sdktrace.WithMaxExportBatchSize(OTLPBatchSize), sdktrace.WithBatchTimeout(OTLPDeadline), sdktrace.WithExportTimeout(OTLPDeadline))
		o.traces = sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(processor))
		o.tracer = o.traces.Tracer("dense-mem.operations")
	}
	if options.MetricEndpoint != "" {
		exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(options.MetricEndpoint), otlpmetrichttp.WithHeaders(options.MetricHeaders), otlpmetrichttp.WithTimeout(OTLPDeadline), otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: false}), otlpmetrichttp.WithHTTPClient(exportHTTPClient()))
		if err != nil {
			_ = o.Shutdown(context.Background())
			return nil, errors.New("OTLP metric initialization failed")
		}
		models := make(map[string]bool)
		modelPattern := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)
		for _, model := range options.Models {
			if options.Protector == nil || !modelPattern.MatchString(model) {
				continue
			}
			protected := options.Protector.Snapshot(model, 1024)
			if protected.UnavailableReason == CredentialProtectionAvailable && protected.Value == model {
				models[model] = true
			}
		}
		producer := prombridge.NewMetricProducer(prombridge.WithGatherer(exportGatherer{source: metrics.registry, models: models, health: &o.metricHealth}))
		reader := sdkmetric.NewPeriodicReader(&localMetricExporter{Exporter: exporter, health: &o.metricHealth, resource: res}, sdkmetric.WithInterval(OTLPMetricInterval), sdkmetric.WithTimeout(OTLPDeadline), sdkmetric.WithProducer(producer))
		o.metrics = sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
	}
	for _, collector := range []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "densemem_otlp_pending_spans", Help: "Locally pending optional trace exports."}, func() float64 { return float64(o.pending.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "densemem_otlp_dropped_spans_total", Help: "Spans dropped before admission to the bounded SDK queue."}, func() float64 { return float64(o.dropped.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "densemem_otlp_trace_failures_total", Help: "Failed optional trace export batches."}, func() float64 { return float64(o.traceHealth.failures.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "densemem_otlp_metric_failures_total", Help: "Failed optional metric export batches."}, func() float64 { return float64(o.metricHealth.failures.Load()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "densemem_otlp_metric_collection_failures_total", Help: "Failed optional export metric collections."}, func() float64 { return float64(o.metricHealth.collectionFailures.Load()) }),
	} {
		if err := metrics.registry.Register(collector); err != nil {
			_ = o.Shutdown(context.Background())
			return nil, errors.New("OTLP local health registration failed")
		}
	}
	return o, nil
}

func (o *OTLP) ObserveOperation(_ context.Context, name string, started time.Time, outcome string) {
	if o == nil || o.tracer == nil {
		return
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return
	}
	for {
		pending := o.pending.Load()
		if pending >= OTLPSpanQueueSize {
			o.dropped.Add(1)
			return
		}
		if o.pending.CompareAndSwap(pending, pending+1) {
			break
		}
	}
	name = exportAllowed(name, "remember recall_memory trace_memory retract_evidence correct_relationship verify resolve_evidence_conflict submit_recall_session_feedback resolve_dream_feedback list_dreams get_dream confirm_dream list_conflicts resolve_conflict export_memory_pack")
	outcome = exportAllowed(outcome, "success cancelled rpc_error tool_error missing_result")
	_, span := o.tracer.Start(context.Background(), "mcp."+name, trace.WithTimestamp(started), trace.WithAttributes(attribute.String("operation", name), attribute.String("outcome", outcome)))
	if outcome != "success" {
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

func (o *OTLP) Health() ExportHealth {
	if o == nil {
		return ExportHealth{QueueCapacity: OTLPSpanQueueSize}
	}
	return ExportHealth{Traces: o.traceHealth.snapshot(), Metrics: o.metricHealth.snapshot(), PendingSpans: o.pending.Load(), DroppedSpans: o.dropped.Load(), QueueCapacity: OTLPSpanQueueSize}
}

func (o *OTLP) ForceFlush(ctx context.Context) error {
	if o == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, OTLPDeadline)
	defer cancel()
	var errs []error
	if o.traces != nil {
		errs = append(errs, o.traces.ForceFlush(ctx))
	}
	if o.metrics != nil {
		errs = append(errs, o.metrics.ForceFlush(ctx))
	}
	return errors.Join(errs...)
}

func (o *OTLP) Shutdown(ctx context.Context) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, OTLPDeadline)
	defer cancel()
	var errs []error
	if o.traces != nil {
		errs = append(errs, o.traces.Shutdown(ctx))
	}
	if o.metrics != nil {
		errs = append(errs, o.metrics.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

type localTraceExporter struct {
	sdktrace.SpanExporter
	owner    *OTLP
	resource *resource.Resource
}

type exportSpan struct {
	sdktrace.ReadOnlySpan
	resource *resource.Resource
}

func (s exportSpan) Resource() *resource.Resource { return s.resource }

func (e *localTraceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	// SDK 1.46 merges environment resources even with WithResource, so project the outbound resource again.
	safe := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		safe[i] = exportSpan{ReadOnlySpan: span, resource: e.resource}
	}
	err := e.SpanExporter.ExportSpans(ctx, safe)
	e.owner.pending.Add(-int64(len(spans)))
	e.owner.traceHealth.record(err)
	// Optional failures remain visible in health and transition logs instead of the SDK's per-batch error logger.
	return nil
}

type localMetricExporter struct {
	sdkmetric.Exporter
	health   *exportHealthState
	resource *resource.Resource
}

func (e *localMetricExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	data.Resource = e.resource
	err := e.Exporter.Export(ctx, data)
	e.health.record(err)
	return nil
}
