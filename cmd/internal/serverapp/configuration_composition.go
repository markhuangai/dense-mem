package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/service"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

func buildConfigurationApplication(repo settingscontract.AppConfigRepository, audit service.AuditService) *settings.AppConfigServiceImpl {
	return settings.NewAppConfigService(repo, audit)
}
