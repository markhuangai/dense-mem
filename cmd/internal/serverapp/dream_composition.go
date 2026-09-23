package serverapp

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/dream"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

type dreamApplicationDependencies struct {
	Remember            rememberapp.Service
	Store               dreamcontract.DreamRepository
	ScheduledStore      dreamcontract.ScheduledDreamRepository
	AppConfig           dream.AppConfig
	Teams               dream.TeamService
	GeneratorTransport  modelprovider.StructuredTransport
	EvidenceStore       dreamcontract.EvidenceDiscoveryRepository
	Diagnostics         dreamcontract.DreamDiagnosticRepository
	DiagnosticProtector observability.DiagnosticProtector
	GraphModel          string
	EvidenceModel       string
	Limits              assessor.SemanticAssessmentLimits
	Metrics             observability.DiscoverabilityMetrics
	Logger              observability.LogProvider
	ProviderCycleLease  time.Duration
}

func dreamProviderCycleLease(cfg config.Config) time.Duration {
	return time.Duration(cfg.GetAIVerifierTimeoutSeconds())*time.Second*
		time.Duration(dreamgeneration.DreamGenerationMaxProviderTurns) + time.Minute
}

func buildDreamApplication(deps dreamApplicationDependencies) dream.Service {
	store := deps.Store
	scheduledStore := deps.ScheduledStore
	evidenceStore := deps.EvidenceStore
	return dream.New(dream.Dependencies{
		Remember:            deps.Remember,
		Store:               store,
		ScheduledStore:      scheduledStore,
		AppConfig:           deps.AppConfig,
		Teams:               deps.Teams,
		Generator:           dream.NewProviderGenerator(dreamgeneration.NewProvider(deps.GeneratorTransport, deps.GraphModel, deps.Limits)),
		EvidenceStore:       evidenceStore,
		EvidenceGenerator:   dream.NewEvidenceProviderGenerator(deps.GeneratorTransport, deps.EvidenceModel, deps.Limits),
		Diagnostics:         deps.Diagnostics,
		DiagnosticProtector: deps.DiagnosticProtector,
		Metrics:             deps.Metrics,
		Logger:              deps.Logger,
		ProviderCycleLease:  deps.ProviderCycleLease,
	})
}

type controlDreamApplicationDependencies struct {
	Store     dreamcontract.DreamControlRepository
	AppConfig dream.AppConfig
	Teams     dream.TeamConfigService
}

func buildControlDreamApplication(deps controlDreamApplicationDependencies) dream.ControlService {
	return dream.NewControl(dream.ControlDependencies{
		Store:     deps.Store,
		AppConfig: deps.AppConfig,
		Teams:     deps.Teams,
	})
}
