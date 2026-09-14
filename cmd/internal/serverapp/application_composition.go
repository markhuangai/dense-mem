package serverapp

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/dream"
	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	"github.com/markhuangai/dense-mem/internal/graph"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/lifecycle"
	"github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	"github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

type applicationCompositionDependencies struct {
	Knowledge              *knowledgepostgres.Store
	Dream                  *dreampostgres.Store
	RememberPersistence    remembercontract.Persistence
	GraphStore             graphcontract.Store
	TraceStore             traceapp.SemanticTraceStore
	RememberCatalog        remembercontract.SubmissionAssessmentCatalog
	Search                 searchcontract.SearchRepository
	RecallSearch           recallcontract.SearchRepository
	RecallFeedbackEvents   recallcontract.FeedbackEventRepository
	Assessor               assessor.Provider
	GeneratorTransport     modelprovider.StructuredTransport
	EmbeddingProvider      embeddingcontract.EmbeddingProviderInterface
	RetryEmbeddingProvider embeddingcontract.EmbeddingProviderInterface
	AssessmentLimits       assessor.SemanticAssessmentLimits
	Metrics                observability.DiscoverabilityMetrics
	Logger                 observability.LogProvider
	Audit                  securityRejectionAuditAppender
	AppConfig              settings.AppConfigService
	Teams                  dream.TeamService
	CommunitySummary       communityapp.SummaryProvider
	CommunityStore         communitycontract.CommunityRepository
	DreamEvidenceStore     dreampostgres.EvidenceDiscoveryRepository
	DreamModel             string
	ProviderCycleLease     time.Duration
	CorrectionTimeout      time.Duration
	CorrectionExecutor     lifecycle.LifecycleCorrectionExecutor
	TelemetryPrometheus    *operations.PrometheusTelemetryService
}

type applicationBundle struct {
	Remember       rememberapp.Service
	Recall         recall.RecallService
	Community      communityapp.Service
	Lifecycle      lifecycle.LifecycleService
	Context        traceapp.Service
	Dream          dream.Service
	ControlDream   dream.ControlService
	Graph          graph.Service
	MemoryPack     memorypack.MemoryPackService
	RecallFeedback *recall.RecallFeedbackEventServiceImpl
}

func buildApplicationBundle(deps applicationCompositionDependencies) applicationBundle {
	rememberService := buildRememberApplication(rememberApplicationDependencies{
		Persistence: deps.RememberPersistence,
		Catalog:     deps.RememberCatalog,
		Assessor:    deps.Assessor,
		Embedder:    deps.EmbeddingProvider,
		Limits:      deps.AssessmentLimits,
		Metrics:     deps.Metrics,
		Logger:      deps.Logger,
		Audit:       deps.Audit,
	})
	recallService := buildRecallApplication(recallApplicationDependencies{
		Search:          deps.RecallSearch,
		Provider:        deps.RetryEmbeddingProvider,
		Hypotheses:      deps.Dream,
		Communities:     deps.CommunityStore,
		CommunityConfig: deps.AppConfig,
		Metrics:         deps.Metrics,
	})
	communityService := buildCommunityApplication(communityApplicationDependencies{
		Store:     deps.CommunityStore,
		AppConfig: deps.AppConfig,
		Summary:   deps.CommunitySummary,
		Metrics:   deps.Metrics,
	})
	lifecycleService := buildLifecycleApplication(lifecycleApplicationDependencies{
		Port:                       deps.Knowledge,
		CorrectionExecutor:         deps.CorrectionExecutor,
		CorrectionEmbeddingTimeout: deps.CorrectionTimeout,
	})
	contextService := buildContextApplication(deps.TraceStore)
	dreamService := buildDreamApplication(dreamApplicationDependencies{
		Remember:           rememberService,
		Store:              deps.Dream,
		ScheduledStore:     deps.Dream,
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
		Store:     deps.Dream,
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
		Graph:          buildGraphApplication(deps.GraphStore),
		MemoryPack:     buildMemoryPackApplication(memoryPackApplicationDependencies{Trace: deps.TraceStore}),
		RecallFeedback: buildRecallFeedbackApplication(recallFeedbackApplicationDependencies{Events: deps.RecallFeedbackEvents, Config: deps.AppConfig}),
	}
}

func configureTelemetryFeatures(prometheus *operations.PrometheusTelemetryService, appConfig settings.AppConfigService, dreams dream.Service) {
	operations.ConfigureTelemetryFeatures(prometheus, appConfig, dreams)
}
