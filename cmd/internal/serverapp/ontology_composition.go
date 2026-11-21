package serverapp

import (
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type ontologyMaintenanceStore interface {
	contract.MaintenanceRepository
	contract.OrganizationRepository
}

func buildOntologyMaintenance(store ontologyMaintenanceStore, config ontology.MaintenanceConfigSource, transport modelprovider.StructuredTransport, limits assessor.SemanticAssessmentLimits, model string, timeout time.Duration, audit ontology.MaintenanceAuditPreparer, logger *slog.Logger, diagnostics ontology.MaintenanceDiagnosticCapture, cooldown ...*ontology.PreparationCooldown) *ontology.MaintenanceService {
	var preparationCooldown *ontology.PreparationCooldown
	if len(cooldown) > 0 {
		preparationCooldown = cooldown[0]
	}
	return ontology.NewMaintenanceService(ontology.MaintenanceDependencies{Repository: store, Config: config, DefaultModel: model, ProviderTimeout: timeout, Audit: audit, Logger: logger,
		Diagnostics: diagnostics, PreparationCooldown: preparationCooldown, Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *ontology.Service {
			configured := limits
			configured.ProviderModel = model
			return ontology.NewService(store, assessment.NewProviderWithAccounting(transport, model, configured, accounting, int(contextWindowTokens))).WithDiagnostics(diagnostics.Observe)
		},
	})
}
