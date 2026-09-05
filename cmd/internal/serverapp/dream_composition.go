package serverapp

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

type dreamApplicationDependencies struct {
	Remember           rememberapp.Service
	Store              repository.DreamRepository
	ScheduledStore     repository.ScheduledDreamRepository
	AppConfig          dreamservice.AppConfig
	Teams              dreamservice.TeamService
	GeneratorTransport modelprovider.StructuredTransport
	EvidenceStore      repository.EvidenceDiscoveryRepository
	Model              string
	Limits             assessor.SemanticAssessmentLimits
	Metrics            observability.DiscoverabilityMetrics
	ProviderCycleLease time.Duration
}

func dreamProviderCycleLease(cfg config.Config) time.Duration {
	return time.Duration(cfg.GetAIVerifierTimeoutSeconds())*time.Second*
		time.Duration(dreamgeneration.DreamGenerationMaxProviderTurns) + time.Minute
}

func buildDreamApplication(deps dreamApplicationDependencies) dreamservice.Service {
	return dreamservice.New(dreamservice.Dependencies{
		Remember:           deps.Remember,
		Store:              deps.Store,
		ScheduledStore:     deps.ScheduledStore,
		AppConfig:          deps.AppConfig,
		Teams:              deps.Teams,
		Generator:          dreamservice.NewProviderGenerator(dreamgeneration.NewProvider(deps.GeneratorTransport, deps.Model, deps.Limits)),
		EvidenceStore:      deps.EvidenceStore,
		EvidenceGenerator:  dreamservice.NewEvidenceProviderGenerator(deps.GeneratorTransport, deps.Model, deps.Limits),
		Metrics:            deps.Metrics,
		ProviderCycleLease: deps.ProviderCycleLease,
	})
}

type controlDreamApplicationDependencies struct {
	Store     repository.DreamControlRepository
	AppConfig dreamservice.AppConfig
	Teams     dreamservice.TeamConfigService
}

func buildControlDreamApplication(deps controlDreamApplicationDependencies) dreamservice.ControlService {
	return dreamservice.NewControl(dreamservice.ControlDependencies{
		Store:     deps.Store,
		AppConfig: deps.AppConfig,
		Teams:     deps.Teams,
	})
}
