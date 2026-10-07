package serverapp

import (
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

func buildOntologyMaintenance(store ontologyMaintenanceStore, config ontology.MaintenanceConfigSource, transport modelprovider.StructuredTransport, limits assessor.SemanticAssessmentLimits, model string, timeout time.Duration, audit ontology.MaintenanceAuditPreparer) *ontology.MaintenanceService {
	return ontology.NewMaintenanceService(ontology.MaintenanceDependencies{Repository: store, Config: config, DefaultModel: model, ProviderTimeout: timeout, Audit: audit,
		Organizer: func(model string, accounting assessment.AttemptAccounting) *ontology.Service {
			configured := limits
			configured.ProviderModel = model
			return ontology.NewService(store, assessment.NewProviderWithAccounting(transport, model, configured, accounting))
		},
	})
}
