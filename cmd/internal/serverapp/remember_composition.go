package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/assessor"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	rememberprocessor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
)

type rememberApplicationDependencies struct {
	Persistence remembercontract.Persistence
	Catalog     remembercontract.SubmissionAssessmentCatalog
	Assessor    assessor.Provider
	Embedder    embeddingcontract.EmbeddingProviderInterface
	Limits      assessor.SemanticAssessmentLimits
	Metrics     observability.DiscoverabilityMetrics
	Logger      observability.LogProvider
	Audit       securityRejectionAuditAppender
}

func buildRememberApplication(deps rememberApplicationDependencies) rememberapp.Service {
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: deps.Persistence, Catalog: deps.Catalog, Assessor: deps.Assessor,
		Embedder: deps.Embedder, Limits: deps.Limits, Metrics: deps.Metrics,
		Logger: deps.Logger, IsStaleInput: rememberapp.IsRememberStaleInputError,
		CommitFailureStage: knowledgepostgres.RememberCommitFailureStage,
	})
	return rememberapp.NewService(rememberapp.Dependencies{
		Synchronous: processor,
		Auditor:     newRememberSecurityRejectionAuditAdapter(deps.Audit),
		Metrics:     deps.Metrics,
		Logger:      deps.Logger,
	})
}

func buildRememberAttemptDiagnostics(repo remembercontract.DiagnosticsRepository) *rememberapp.RememberAttemptDiagnosticsService {
	return rememberapp.NewRememberAttemptDiagnosticsService(repo)
}
