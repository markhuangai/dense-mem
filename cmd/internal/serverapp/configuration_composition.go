package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

func buildConfigurationApplication(repo settingscontract.AppConfigRepository, audit accessservice.AuditService) *settings.AppConfigServiceImpl {
	return settings.NewAppConfigService(repo, audit)
}

func buildDecisionConfigurationApplication(repo settingscontract.AppConfigRepository, audit accessservice.AuditService, cfg config.Config, logger observability.LogProvider) *settings.AppConfigServiceImpl {
	return settings.NewAppConfigServiceWithDecisions(repo, audit, settings.DecisionSettingsOptions{Provider: domain.DecisionProviderStatus{
		Configured: cfg.Decision.Configured(), Provider: cfg.Decision.Provider, Model: cfg.Decision.Model, MaxInputTokens: cfg.Decision.MaxInputTokens, MaxQuestionInputTokens: cfg.Decision.MaxQuestionInputTokens, LongContextEnabled: cfg.Decision.LongContextEnabled,
	}, Logger: logger})
}
