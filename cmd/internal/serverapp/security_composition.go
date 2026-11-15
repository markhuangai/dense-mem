package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/observability"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

func buildSecurityApplication(repo settingscontract.SecurityRepository, audit accessservice.AuditService, protector observability.DiagnosticProtector, logger observability.LogProvider) *settings.SecurityServiceImpl {
	return settings.NewSecurityServiceWithDiagnostics(repo, audit, settings.SecurityDiagnosticsOptions{Protector: protector, Logger: logger})
}
