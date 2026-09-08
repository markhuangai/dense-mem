package http

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/dream"
	"github.com/markhuangai/dense-mem/internal/service"
)

func effectiveDreamingConfig(ctx context.Context, appConfig service.AppConfigService, teamConfig map[string]any) (*dream.EffectiveConfig, error) {
	var global domain.DreamingRuntimeConfig
	if appConfig != nil {
		runtime, err := appConfig.DreamingRuntimeConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("load dreaming runtime config: %w", err)
		}
		global = runtime
	}
	effective, err := dream.EffectiveDreamingConfig(global, teamConfig)
	if err != nil {
		return nil, fmt.Errorf("compute effective dreaming config: %w", err)
	}
	return &effective, nil
}
