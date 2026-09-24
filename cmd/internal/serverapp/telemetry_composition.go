package serverapp

import (
	"context"
	nethttp "net/http"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	conflictpostgres "github.com/markhuangai/dense-mem/internal/conflict/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
)

type telemetryComposition struct {
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
) (telemetryComposition, error) {
	composition := telemetryComposition{Metrics: observability.NoopDiscoverabilityMetrics()}
	if !cfg.GetTelemetryEnabled() {
		return composition, nil
	}
	if err := operations.RefreshTelemetryPricingCache(startupCtx, pricing); err != nil {
		logger.Warn("telemetry pricing snapshot unavailable at startup", observability.String("reason", "configuration_refresh_failed"))
	}
	composition.PricingRefreshEnabled = true
	prometheusMetrics := observability.NewPrometheusMetrics(operations.NewTelemetryPricingResolver(pricing, cfg.GetAIVerifierModel()))
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
	composition.Prometheus = operations.NewPrometheusTelemetryServiceWithJobAndLogger(
		cfg.GetTelemetryPrometheusURL(),
		time.Duration(cfg.GetTelemetryQueryTimeoutSeconds())*time.Second,
		cfg.GetTelemetryPrometheusJob(),
		logger,
	)
	composition.Prometheus.SetLifecycleReader(lifecycle)
	composition.Reader = composition.Prometheus
	return composition, nil
}
