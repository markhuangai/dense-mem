package serverapp

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/dream"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

type dreamApplicationDependencies struct {
	Remember           rememberapp.Service
	Store              dreamcontract.DreamRepository
	ScheduledStore     dreamcontract.ScheduledDreamRepository
	AppConfig          dream.AppConfig
	Teams              dream.TeamService
	GeneratorTransport modelprovider.StructuredTransport
	EvidenceStore      dreamcontract.EvidenceDiscoveryRepository
	Model              string
	Limits             assessor.SemanticAssessmentLimits
	Metrics            observability.DiscoverabilityMetrics
	ProviderCycleLease time.Duration
}

func dreamProviderCycleLease(cfg config.Config) time.Duration {
	return time.Duration(cfg.GetAIVerifierTimeoutSeconds())*time.Second*
		time.Duration(dreamgeneration.DreamGenerationMaxProviderTurns) + time.Minute
}

func buildDreamApplication(deps dreamApplicationDependencies) dream.Service {
	store := deps.Store
	scheduledStore := deps.ScheduledStore
	evidenceStore := deps.EvidenceStore
	if source, ok := deps.Store.(dreampostgres.Source); ok {
		native := dreampostgres.NewStoreFromSource(source)
		store = native
		scheduledStore = native
		evidenceStore = native
	}
	return dream.New(dream.Dependencies{
		Remember:           deps.Remember,
		Store:              store,
		ScheduledStore:     scheduledStore,
		AppConfig:          deps.AppConfig,
		Teams:              deps.Teams,
		Generator:          dream.NewProviderGenerator(dreamgeneration.NewProvider(deps.GeneratorTransport, deps.Model, deps.Limits)),
		EvidenceStore:      evidenceStore,
		EvidenceGenerator:  dream.NewEvidenceProviderGenerator(deps.GeneratorTransport, deps.Model, deps.Limits),
		Metrics:            deps.Metrics,
		ProviderCycleLease: deps.ProviderCycleLease,
	})
}

type controlDreamApplicationDependencies struct {
	Store     dreamcontract.DreamControlRepository
	AppConfig dream.AppConfig
	Teams     dream.TeamConfigService
}

func buildControlDreamApplication(deps controlDreamApplicationDependencies) dream.ControlService {
	store := deps.Store
	if source, ok := deps.Store.(dreampostgres.Source); ok {
		store = dreampostgres.NewStoreFromSource(source)
	}
	return dream.NewControl(dream.ControlDependencies{
		Store:     store,
		AppConfig: deps.AppConfig,
		Teams:     deps.Teams,
	})
}
