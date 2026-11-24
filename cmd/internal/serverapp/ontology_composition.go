package serverapp

import (
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
)

type topicMaintenanceStore interface {
	contract.MaintenanceRepository
	contract.OrganizationRepository
}

func buildTopicMaintenance(store topicMaintenanceStore, config ontology.MaintenanceConfigSource, transport modelprovider.StructuredTransport, limits assessor.SemanticAssessmentLimits, model string, timeout time.Duration, audit ontology.MaintenanceAuditPreparer, logger *slog.Logger, diagnostics ontology.MaintenanceDiagnosticCapture) *ontology.MaintenanceService {
	return ontology.NewMaintenanceService(ontology.MaintenanceDependencies{Repository: store, Config: config, DefaultModel: model, ProviderTimeout: timeout, Audit: audit, Logger: logger,
		Diagnostics: diagnostics, Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *ontology.Service {
			configured := limits
			configured.ProviderModel = model
			return ontology.NewService(store, assessment.NewProviderWithAccounting(transport, model, configured, accounting, int(contextWindowTokens)).WithLogger(logger)).WithDiagnostics(diagnostics.Observe)
		},
	})
}
