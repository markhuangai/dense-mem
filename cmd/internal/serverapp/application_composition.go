package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/assessor"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
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
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	"github.com/markhuangai/dense-mem/internal/topics"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

type applicationCompositionDependencies struct {
	TopicAssignment             *topics.AssignmentService
	PublicSeedAdmission         *rememberapp.PublicSeedAdmission
	Knowledge                   *knowledgepostgres.Store
	SessionRepository           session.Repository
	RememberPersistence         remembercontract.Persistence
	TraceStore                  traceapp.SemanticTraceStore
	MemoryPackReader            memorypack.MemoryPackSemanticReader
	Search                      searchcontract.SearchRepository
	RecallSearch                recallcontract.SearchRepository
	RecallFeedbackEvents        recallcontract.FeedbackEventRepository
	Assessor                    assessor.Provider
	Decisions                   modelprovider.DecisionProvider
	DecisionSettings            modelprovider.DecisionSettings
	SessionAssessor             assessor.Provider
	SessionEnabled              bool
	RecallExpansionEnabled      bool
	RecallDisableQueryEmbedding bool
	RememberModel               string
	GeneratorTransport          modelprovider.StructuredTransport
	EmbeddingProvider           embeddingcontract.EmbeddingProviderInterface
	RetryEmbeddingProvider      embeddingcontract.EmbeddingProviderInterface
	AssessmentLimits            assessor.SemanticAssessmentLimits
	Metrics                     observability.DiscoverabilityMetrics
	Logger                      observability.LogProvider
	DiagnosticProtector         observability.DiagnosticProtector
	Audit                       securityRejectionAuditAppender
	AppConfig                   settings.AppConfigService
	TelemetryPrometheus         *operations.PrometheusTelemetryService
}

type applicationBundle struct {
	Remember       rememberapp.Service
	Session        session.API
	Recall         recall.RecallService
	Lifecycle      lifecycle.LifecycleService
	Context        traceapp.Service
	MemoryPack     memorypack.MemoryPackService
	RecallFeedback *recall.RecallFeedbackEventServiceImpl
}

func buildApplicationBundle(deps applicationCompositionDependencies) applicationBundle {
	embeddingExecutor := semanticwrite.NewProviderExecutor(deps.EmbeddingProvider)
	rememberService := buildRememberApplication(rememberApplicationDependencies{
		PublicSeedAdmission: deps.PublicSeedAdmission,
		TopicAssignment:     deps.TopicAssignment,
		Persistence:         deps.RememberPersistence,
		Assessor:            deps.Assessor,
		Decisions:           deps.Decisions,
		DecisionSettings:    deps.DecisionSettings,
		EmbeddingExecutor:   embeddingExecutor,
		Limits:              deps.AssessmentLimits,
		Metrics:             deps.Metrics,
		Logger:              deps.Logger,
		DiagnosticProtector: deps.DiagnosticProtector,
		Audit:               deps.Audit,
	})
	sessionService := buildSessionApplication(rememberApplicationDependencies{
		Persistence: deps.RememberPersistence, Assessor: deps.SessionAssessor,
		Decisions: deps.Decisions, DecisionSettings: deps.DecisionSettings,
		EmbeddingExecutor: embeddingExecutor, Limits: deps.AssessmentLimits, Metrics: deps.Metrics,
		Logger: deps.Logger, DiagnosticProtector: deps.DiagnosticProtector, Audit: deps.Audit,
	}, deps.SessionRepository, deps.GeneratorTransport, deps.RememberModel, deps.SessionEnabled)
	recallService := buildRecallApplication(recallApplicationDependencies{
		DiagnosticProtector:    deps.DiagnosticProtector,
		Search:                 deps.RecallSearch,
		ExpansionEnabled:       deps.RecallExpansionEnabled,
		DisableQueryEmbedding:  deps.RecallDisableQueryEmbedding,
		Provider:               deps.RetryEmbeddingProvider,
		TopicMaintenanceConfig: deps.AppConfig,
		Metrics:                deps.Metrics,
		Decisions:              deps.Decisions, DecisionSettings: deps.DecisionSettings, Logger: deps.Logger,
	})
	lifecycleService := buildLifecycleApplication(lifecycleApplicationDependencies{
		Port: deps.Knowledge,
	})
	contextService := buildContextApplication(deps.TraceStore, deps.Logger)
	configureTelemetryFeatures(deps.TelemetryPrometheus, deps.AppConfig)
	return applicationBundle{
		Remember:       rememberService,
		Session:        sessionService,
		Recall:         recallService,
		Lifecycle:      lifecycleService,
		Context:        contextService,
		MemoryPack:     buildMemoryPackApplication(memoryPackApplicationDependencies{Trace: deps.MemoryPackReader, Logger: deps.Logger}),
		RecallFeedback: buildRecallFeedbackApplication(recallFeedbackApplicationDependencies{Events: deps.RecallFeedbackEvents, Config: deps.AppConfig}),
	}
}

func configureTelemetryFeatures(prometheus *operations.PrometheusTelemetryService, appConfig settings.AppConfigService) {
	operations.ConfigureTelemetryFeatures(prometheus, appConfig)
}
