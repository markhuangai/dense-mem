package serverapp

import (
	"context"
	"errors"
	nethttp "net/http"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	operationsprometheus "github.com/markhuangai/dense-mem/internal/operations/prometheus"
)

type telemetryComposition struct {
	Exports               *observability.OTLP
	Metrics               observability.DiscoverabilityMetrics
	Prometheus            *operations.PrometheusTelemetryService
	HTTPMetrics           observability.HTTPMetrics
	ScrapeHandler         nethttp.Handler
	Reader                operations.TelemetryReader
	PricingRefreshEnabled bool
}

func buildTelemetryApplication(
	startupCtx context.Context,
	cfg config.Config,
	pricing operationscontract.TelemetryPricingReader,
	conflictQueue *conflictpostgres.Store,
	lifecycle operationscontract.TelemetryLifecycleReader,
	logger observability.LogProvider,
	protector *observability.CredentialProtector,
) (telemetryComposition, error) {
	composition := telemetryComposition{Metrics: observability.NoopDiscoverabilityMetrics()}
	if err := cfg.ValidateExports(); err != nil {
		return telemetryComposition{}, err
	}
	if !cfg.GetTelemetryEnabled() {
		return composition, nil
	}
	if protector == nil {
		return telemetryComposition{}, errors.New("telemetry credential protection is unavailable")
	}
	if err := operations.RefreshTelemetryPricingCache(startupCtx, pricing); err != nil {
		logger.Warn("telemetry pricing snapshot unavailable at startup", observability.String("reason", "configuration_refresh_failed"))
	}
	composition.PricingRefreshEnabled = true
	prometheusMetrics := observability.NewPrometheusMetrics(operations.NewTelemetryPricingResolver(pricing, cfg.GetAIVerifierModel(), cfg.GetAIEmbeddingModel()))
	prometheusMetrics.SetCredentialMetadataProtector(protector)
	operationalReader, _ := lifecycle.(operationscontract.OperationalTelemetryReader)
	if err := prometheusMetrics.RegisterOperationalTelemetryCollector(operationalReader); err != nil {
		return telemetryComposition{}, err
	}
	if conflictQueue != nil {
		if err := prometheusMetrics.RegisterConflictQueueCollector(observability.NewConflictQueueCollector(conflictQueue.CollectConflictQueueMetrics)); err != nil {
			return telemetryComposition{}, err
		}
	}
	composition.Metrics = prometheusMetrics
	composition.HTTPMetrics = prometheusMetrics
	composition.ScrapeHandler = prometheusMetrics.Handler()
	timeout := time.Duration(cfg.GetTelemetryQueryTimeoutSeconds()) * time.Second
	composition.Prometheus = operations.NewPrometheusTelemetryService(
		operationsprometheus.NewClient(cfg.GetTelemetryPrometheusURL(), timeout),
		timeout,
		cfg.GetTelemetryPrometheusJob(),
		logger,
	)
	composition.Prometheus.SetLifecycleReader(lifecycle)
	composition.Reader = composition.Prometheus
	if cfg.OTLPEnabled {
		exports, err := observability.NewOTLP(startupCtx, prometheusMetrics, observability.OTLPOptions{
			TraceEndpoint: cfg.OTLPTraceEndpoint, MetricEndpoint: cfg.OTLPMetricEndpoint,
			TraceHeaders: cfg.OTLPTraceHeaders, MetricHeaders: cfg.OTLPMetricHeaders,
			Models:    []string{cfg.GetAIVerifierModel(), cfg.GetAIEmbeddingModel(), cfg.GetAIRememberModel(), cfg.GetAIConflictReviewModel(), cfg.GetAIDreamGraphModel(), cfg.GetAICommunitySummaryModel()},
			Protector: protector, Logger: logger,
		})
		if err != nil {
			return telemetryComposition{}, err
		}
		composition.Exports = exports
	}
	return composition, nil
}
