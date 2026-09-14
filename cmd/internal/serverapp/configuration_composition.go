package serverapp

import (
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

func buildConfigurationApplication(repo settingscontract.AppConfigRepository, audit accessservice.AuditService) *settings.AppConfigServiceImpl {
	return settings.NewAppConfigService(repo, audit)
}
