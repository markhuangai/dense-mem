package serverapp

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

type applicationCompositionDependencies struct {
	Ledger                 *repository.LedgerRepositoryImpl
	Semantic               *repository.SemanticRepositoryImpl
	Search                 *repository.SearchRepositoryImpl
	RecallFeedbackEvents   repository.RecallFeedbackEventRepository
	Assessor               assessor.Provider
	GeneratorTransport     modelprovider.StructuredTransport
	EmbeddingProvider      embeddingcontract.EmbeddingProviderInterface
	RetryEmbeddingProvider embeddingcontract.EmbeddingProviderInterface
	AssessmentLimits       assessor.SemanticAssessmentLimits
	Metrics                observability.DiscoverabilityMetrics
	Logger                 observability.LogProvider
	Audit                  securityRejectionAuditAppender
	AppConfig              service.AppConfigService
	Teams                  dreamservice.TeamService
	CommunitySummary       communityservice.SummaryProvider
	DreamEvidenceStore     repository.EvidenceDiscoveryRepository
	DreamModel             string
	ProviderCycleLease     time.Duration
	CorrectionTimeout      time.Duration
	CorrectionExecutor     memoryservice.LifecycleCorrectionExecutor
	TelemetryPrometheus    *service.PrometheusTelemetryService
}

type applicationBundle struct {
	Remember       remember.Service
	Recall         memoryservice.RecallService
	Community      communityservice.Service
	Lifecycle      memoryservice.LifecycleService
	Context        contextservice.Service
	Dream          dreamservice.Service
	ControlDream   dreamservice.ControlService
	Graph          graphview.Service
	MemoryPack     skillpackservice.MemoryPackService
	RecallFeedback *service.RecallFeedbackEventServiceImpl
}

func buildApplicationBundle(deps applicationCompositionDependencies) applicationBundle {
	rememberService := buildRememberApplication(rememberApplicationDependencies{
		Ledger:   deps.Ledger,
		Catalog:  deps.Semantic,
		Assessor: deps.Assessor,
		Embedder: deps.EmbeddingProvider,
		Limits:   deps.AssessmentLimits,
		Metrics:  deps.Metrics,
		Logger:   deps.Logger,
		Audit:    deps.Audit,
	})
	recallService := buildRecallApplication(recallApplicationDependencies{
		Search:          deps.Search,
		Provider:        deps.RetryEmbeddingProvider,
		Hypotheses:      deps.Semantic,
		Communities:     deps.Semantic,
		CommunityConfig: deps.AppConfig,
		Metrics:         deps.Metrics,
	})
	communityService := buildCommunityApplication(communityApplicationDependencies{
		Store:     deps.Semantic,
		AppConfig: deps.AppConfig,
		Summary:   deps.CommunitySummary,
		Metrics:   deps.Metrics,
	})
	lifecycleService := buildLifecycleApplication(lifecycleApplicationDependencies{
		Semantic:                   deps.Semantic,
		Evidence:                   deps.Ledger,
		CorrectionExecutor:         deps.CorrectionExecutor,
		CorrectionEmbeddingTimeout: deps.CorrectionTimeout,
	})
	contextService := buildContextApplication(deps.Semantic)
	dreamService := buildDreamApplication(dreamApplicationDependencies{
		Remember:           rememberService,
		Store:              deps.Semantic,
		ScheduledStore:     deps.Semantic,
		AppConfig:          deps.AppConfig,
		Teams:              deps.Teams,
		GeneratorTransport: deps.GeneratorTransport,
		EvidenceStore:      deps.DreamEvidenceStore,
		Model:              deps.DreamModel,
		Limits:             deps.AssessmentLimits,
		Metrics:            deps.Metrics,
		ProviderCycleLease: deps.ProviderCycleLease,
	})
	configureTelemetryFeatures(deps.TelemetryPrometheus, deps.AppConfig, dreamService)
	controlDreamService := buildControlDreamApplication(controlDreamApplicationDependencies{
		Store:     deps.Semantic,
		AppConfig: deps.AppConfig,
		Teams:     deps.Teams,
	})
	return applicationBundle{
		Remember:       rememberService,
		Recall:         recallService,
		Community:      communityService,
		Lifecycle:      lifecycleService,
		Context:        contextService,
		Dream:          dreamService,
		ControlDream:   controlDreamService,
		Graph:          buildGraphApplication(deps.Semantic),
		MemoryPack:     buildMemoryPackApplication(memoryPackApplicationDependencies{Semantic: deps.Semantic}),
		RecallFeedback: buildRecallFeedbackApplication(recallFeedbackApplicationDependencies{Events: deps.RecallFeedbackEvents, Config: deps.AppConfig}),
	}
}
