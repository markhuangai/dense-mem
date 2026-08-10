package serverapp

import (
	"context"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func configureTelemetryFeatures(prometheus *service.PrometheusTelemetryService, appConfig service.AppConfigService, dreams dreamservice.Service) {
	if prometheus == nil {
		return
	}
	prometheus.SetFeatureResolver(service.TelemetryFeatureResolver{
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
