package http

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

func effectiveDreamingConfig(ctx context.Context, appConfig service.AppConfigService, teamConfig map[string]any) (*dreamservice.EffectiveConfig, error) {
	var global domain.DreamingRuntimeConfig
	if appConfig != nil {
		runtime, err := appConfig.DreamingRuntimeConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("load dreaming runtime config: %w", err)
		}
		global = runtime
	}
	effective, err := dreamservice.EffectiveDreamingConfig(global, teamConfig)
	if err != nil {
		return nil, fmt.Errorf("compute effective dreaming config: %w", err)
	}
	return &effective, nil
}
