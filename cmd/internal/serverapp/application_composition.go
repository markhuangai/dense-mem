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
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

type applicationCompositionDependencies struct {
	Knowledge              *knowledgepostgres.Store
	RememberPersistence    remembercontract.Persistence
	TraceStore             traceapp.SemanticTraceStore
	Search                 searchcontract.SearchRepository
	RecallSearch           recallcontract.SearchRepository
	RecallFeedbackEvents   recallcontract.FeedbackEventRepository
	Assessor               assessor.Provider
	Decisions              modelprovider.DecisionProvider
	DecisionSettings       modelprovider.DecisionSettings
	SessionAssessor        assessor.Provider
	SessionEnabled         bool
	RememberModel          string
	GeneratorTransport     modelprovider.StructuredTransport
	EmbeddingProvider      embeddingcontract.EmbeddingProviderInterface
	RetryEmbeddingProvider embeddingcontract.EmbeddingProviderInterface
	AssessmentLimits       assessor.SemanticAssessmentLimits
	Metrics                observability.DiscoverabilityMetrics
	Logger                 observability.LogProvider
	DiagnosticProtector    observability.DiagnosticProtector
	Audit                  securityRejectionAuditAppender
	AppConfig              settings.AppConfigService
	TelemetryPrometheus    *operations.PrometheusTelemetryService
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
	}, deps.Knowledge, deps.GeneratorTransport, deps.RememberModel, deps.SessionEnabled)
	recallService := buildRecallApplication(recallApplicationDependencies{
		Search:         deps.RecallSearch,
		Provider:       deps.RetryEmbeddingProvider,
		OntologyConfig: deps.AppConfig,
		Metrics:        deps.Metrics,
		Decisions:      deps.Decisions, DecisionSettings: deps.DecisionSettings, Logger: deps.Logger,
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
		MemoryPack:     buildMemoryPackApplication(memoryPackApplicationDependencies{Trace: deps.TraceStore, Logger: deps.Logger}),
		RecallFeedback: buildRecallFeedbackApplication(recallFeedbackApplicationDependencies{Events: deps.RecallFeedbackEvents, Config: deps.AppConfig}),
	}
}

func configureTelemetryFeatures(prometheus *operations.PrometheusTelemetryService, appConfig settings.AppConfigService) {
	operations.ConfigureTelemetryFeatures(prometheus, appConfig)
}
