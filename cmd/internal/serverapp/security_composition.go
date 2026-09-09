package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/service"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

func buildSecurityApplication(repo settingscontract.SecurityRepository, audit service.AuditService) *settings.SecurityServiceImpl {
	return settings.NewSecurityService(repo, audit)
}
