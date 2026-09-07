package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func buildSecurityApplication(repo repository.SecurityRepository, audit service.AuditService) *service.SecurityServiceImpl {
	return service.NewSecurityService(repo, audit)
}
