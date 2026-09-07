package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func buildConfigurationApplication(repo repository.AppConfigRepository, audit service.AuditService) *service.AppConfigServiceImpl {
	return service.NewAppConfigService(repo, audit)
}
