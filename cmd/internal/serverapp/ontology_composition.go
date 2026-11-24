package serverapp

import (
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	topics "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
)

type topicMaintenanceStore interface {
	contract.MaintenanceRepository
	contract.OrganizationRepository
}

func buildTopicMaintenance(store topicMaintenanceStore, config topics.MaintenanceConfigSource, transport modelprovider.StructuredTransport, limits assessor.SemanticAssessmentLimits, model string, timeout time.Duration, audit topics.MaintenanceAuditPreparer, logger *slog.Logger, diagnostics topics.MaintenanceDiagnosticCapture) *topics.MaintenanceService {
	return topics.NewMaintenanceService(topics.MaintenanceDependencies{Repository: store, Config: config, DefaultModel: model, ProviderTimeout: timeout, Audit: audit, Logger: logger,
		Diagnostics: diagnostics, Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *topics.Service {
			configured := limits
			configured.ProviderModel = model
			return topics.NewService(store, assessment.NewProviderWithAccounting(transport, model, configured, accounting, int(contextWindowTokens)).WithLogger(logger)).WithDiagnostics(diagnostics.Observe)
		},
	})
}
