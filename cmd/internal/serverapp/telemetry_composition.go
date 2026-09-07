package serverapp

import (
	"context"
	"errors"
	nethttp "net/http"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

type telemetryComposition struct {
	Metrics               observability.DiscoverabilityMetrics
	Prometheus            *service.PrometheusTelemetryService
	HTTPMetrics           observability.HTTPMetrics
	ScrapeHandler         nethttp.Handler
	Reader                service.TelemetryReader
	PricingRefreshContext context.Context
	PricingRefreshCancel  context.CancelFunc
}

func buildTelemetryApplication(
	startupCtx context.Context,
	cfg config.Config,
	appConfigService *service.AppConfigServiceImpl,
	conflictQueue repository.ConflictQueueRepository,
	lifecycle repository.TelemetryLifecycleReader,
	logger observability.LogProvider,
) (telemetryComposition, error) {
	composition := telemetryComposition{Metrics: observability.NoopDiscoverabilityMetrics()}
	if !cfg.GetTelemetryEnabled() {
		return composition, nil
	}
	if err := refreshTelemetryPricingCache(startupCtx, appConfigService); err != nil {
		logger.Warn("telemetry pricing snapshot unavailable at startup", observability.String("reason", "configuration_refresh_failed"))
	}
	pricingRefreshCtx, cancel := context.WithCancel(context.Background())
	composition.PricingRefreshContext = pricingRefreshCtx
	composition.PricingRefreshCancel = cancel
	prometheusMetrics := observability.NewPrometheusMetrics(observability.AIPricingResolverFunc(func(ctx context.Context) (observability.AIPricing, error) {
		pricing, ok := appConfigService.CachedTelemetryPricingRuntimeConfig()
		if !ok {
			return observability.AIPricing{}, errors.New("telemetry pricing snapshot unavailable")
		}
		return observability.AIPricing{
			VerifierInputUSDPerMillionTokens:  pricing.VerifierInputUSDPerMillionTokens,
			VerifierOutputUSDPerMillionTokens: pricing.VerifierOutputUSDPerMillionTokens,
			EmbeddingInputUSDPerMillionTokens: pricing.EmbeddingInputUSDPerMillionTokens,
		}, nil
	}))
	if conflictQueue != nil {
		if err := prometheusMetrics.RegisterConflictQueueCollector(observability.NewConflictQueueCollector(conflictQueue.CollectConflictQueueMetrics)); err != nil {
			cancel()
			return telemetryComposition{}, err
		}
	}
	composition.Metrics = prometheusMetrics
	composition.HTTPMetrics = prometheusMetrics
	composition.ScrapeHandler = prometheusMetrics.Handler()
	composition.Prometheus = service.NewPrometheusTelemetryServiceWithJobAndLogger(
		cfg.GetTelemetryPrometheusURL(),
		time.Duration(cfg.GetTelemetryQueryTimeoutSeconds())*time.Second,
		cfg.GetTelemetryPrometheusJob(),
		logger,
	)
	composition.Prometheus.SetLifecycleReader(lifecycle)
	composition.Reader = composition.Prometheus
	return composition, nil
}

const telemetryPricingRefreshTimeout = 5 * time.Second

func refreshTelemetryPricingCache(ctx context.Context, appConfigService *service.AppConfigServiceImpl) error {
	if appConfigService == nil {
		return errors.New("telemetry pricing configuration is unavailable")
	}
	refreshCtx, cancel := context.WithTimeout(ctx, telemetryPricingRefreshTimeout)
	defer cancel()
	_, err := appConfigService.TelemetryPricingRuntimeConfig(refreshCtx)
	return err
}

func refreshTelemetryPricingCacheUntilCanceled(ctx context.Context, appConfigService *service.AppConfigServiceImpl, logger observability.LogProvider) {
	ticker := time.NewTicker(service.DefaultAppConfigCacheCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refreshTelemetryPricingCache(ctx, appConfigService); err != nil {
				logger.Warn("telemetry pricing snapshot refresh failed", observability.String("reason", "configuration_refresh_failed"))
			}
		}
	}
}
