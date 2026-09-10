package serverapp

import (
	"context"
	nethttp "net/http"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type telemetryComposition struct {
	Metrics               observability.DiscoverabilityMetrics
	Prometheus            *operations.PrometheusTelemetryService
	HTTPMetrics           observability.HTTPMetrics
	ScrapeHandler         nethttp.Handler
	Reader                operations.TelemetryReader
	PricingRefreshContext context.Context
	PricingRefreshCancel  context.CancelFunc
}

func buildTelemetryApplication(
	startupCtx context.Context,
	cfg config.Config,
	pricing operationscontract.TelemetryPricingReader,
	conflictQueue repository.ConflictQueueRepository,
	lifecycle operationscontract.TelemetryLifecycleReader,
	logger observability.LogProvider,
) (telemetryComposition, error) {
	composition := telemetryComposition{Metrics: observability.NoopDiscoverabilityMetrics()}
	if !cfg.GetTelemetryEnabled() {
		return composition, nil
	}
	if err := refreshTelemetryPricingCache(startupCtx, pricing); err != nil {
		logger.Warn("telemetry pricing snapshot unavailable at startup", observability.String("reason", "configuration_refresh_failed"))
	}
	pricingRefreshCtx, cancel := context.WithCancel(context.Background())
	composition.PricingRefreshContext = pricingRefreshCtx
	composition.PricingRefreshCancel = cancel
	prometheusMetrics := observability.NewPrometheusMetrics(operations.NewTelemetryPricingResolver(pricing))
	if conflictQueue != nil {
		if err := prometheusMetrics.RegisterConflictQueueCollector(observability.NewConflictQueueCollector(conflictQueue.CollectConflictQueueMetrics)); err != nil {
			cancel()
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

func refreshTelemetryPricingCache(ctx context.Context, pricing operationscontract.TelemetryPricingReader) error {
	return operations.RefreshTelemetryPricingCache(ctx, pricing)
}

func refreshTelemetryPricingCacheUntilCanceled(ctx context.Context, pricing operationscontract.TelemetryPricingReader, logger observability.LogProvider) {
	operations.RefreshTelemetryPricingCacheUntilCanceled(ctx, pricing, logger)
}
