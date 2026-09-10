package operations

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/dream"
	"github.com/markhuangai/dense-mem/internal/observability"
	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	settings "github.com/markhuangai/dense-mem/internal/settings"
)

const telemetryPricingRefreshTimeout = 5 * time.Second

func RefreshTelemetryPricingCache(ctx context.Context, pricing operationscontract.TelemetryPricingReader) error {
	if pricing == nil {
		return errors.New("telemetry pricing configuration is unavailable")
	}
	refreshCtx, cancel := context.WithTimeout(ctx, telemetryPricingRefreshTimeout)
	defer cancel()
	_, err := pricing.TelemetryPricingRuntimeConfig(refreshCtx)
	return err
}

func RefreshTelemetryPricingCacheUntilCanceled(ctx context.Context, pricing operationscontract.TelemetryPricingReader, logger observability.LogProvider) {
	ticker := time.NewTicker(settings.DefaultAppConfigCacheCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := RefreshTelemetryPricingCache(ctx, pricing); err != nil && logger != nil {
				logger.Warn("telemetry pricing snapshot refresh failed", observability.String("reason", "configuration_refresh_failed"))
			}
		}
	}
}

// TelemetryPricingResolver converts the settings-owned rate card into the
// observability port without exposing settings implementation details.
type TelemetryPricingResolver struct {
	pricing operationscontract.TelemetryPricingReader
}

func NewTelemetryPricingResolver(pricing operationscontract.TelemetryPricingReader) observability.AIPricingResolver {
	return TelemetryPricingResolver{pricing: pricing}
}

func (r TelemetryPricingResolver) ResolveAIPricing(context.Context) (observability.AIPricing, error) {
	if r.pricing == nil {
		return observability.AIPricing{}, errors.New("telemetry pricing snapshot unavailable")
	}
	pricing, ok := r.pricing.CachedTelemetryPricingRuntimeConfig()
	if !ok {
		return observability.AIPricing{}, errors.New("telemetry pricing snapshot unavailable")
	}
	return observability.AIPricing{
		VerifierInputUSDPerMillionTokens:  clonePrice(pricing.VerifierInputUSDPerMillionTokens),
		VerifierOutputUSDPerMillionTokens: clonePrice(pricing.VerifierOutputUSDPerMillionTokens),
		EmbeddingInputUSDPerMillionTokens: clonePrice(pricing.EmbeddingInputUSDPerMillionTokens),
	}, nil
}

func clonePrice(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func ConfigureTelemetryFeatures(prometheus *PrometheusTelemetryService, appConfig settings.AppConfigService, dreams dream.Service) {
	if prometheus == nil {
		return
	}
	prometheus.SetFeatureResolver(TelemetryFeatureResolver{
		RecallFeedbackEnabled: func(ctx context.Context) (bool, error) {
			config, err := appConfig.RecallFeedbackRuntimeConfig(ctx)
			return config.Enabled, err
		},
		DreamingEnabled: func(ctx context.Context, teamID *uuid.UUID) (bool, error) {
			if teamID == nil {
				config, err := appConfig.DreamingRuntimeConfig(ctx)
				return config.Enabled, err
			}
			effective, err := dreams.EffectiveConfig(ctx, teamID.String())
			return effective.Enabled, err
		},
	})
}
