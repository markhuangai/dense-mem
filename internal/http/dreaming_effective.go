package http

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func effectiveDreamingConfig(ctx context.Context, appConfig service.AppConfigService, teamConfig map[string]any) *dreamservice.EffectiveConfig {
	var global domain.DreamingRuntimeConfig
	if appConfig != nil {
		runtime, err := appConfig.DreamingRuntimeConfig(ctx)
		if err != nil {
			return nil
		}
		global = runtime
	}
	effective, err := dreamservice.EffectiveDreamingConfig(global, teamConfig)
	if err != nil {
		return nil
	}
	return &effective
}
