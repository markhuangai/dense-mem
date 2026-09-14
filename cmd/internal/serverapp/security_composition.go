package serverapp

import (
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

func buildSecurityApplication(repo settingscontract.SecurityRepository, audit accessservice.AuditService) *settings.SecurityServiceImpl {
	return settings.NewSecurityService(repo, audit)
}
