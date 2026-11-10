package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/assessor"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	rememberprocessor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

type rememberApplicationDependencies struct {
	Persistence         remembercontract.Persistence
	Assessor            assessor.Provider
	Decisions           modelprovider.DecisionProvider
	DecisionSettings    modelprovider.DecisionSettings
	EmbeddingExecutor   *semanticwrite.Executor
	Limits              assessor.SemanticAssessmentLimits
	Metrics             observability.DiscoverabilityMetrics
	Logger              observability.LogProvider
	DiagnosticProtector observability.DiagnosticProtector
	Audit               securityRejectionAuditAppender
}

func buildRememberApplication(deps rememberApplicationDependencies) rememberapp.Service {
	processor := rememberprocessor.NewSynchronousProcessor(rememberprocessor.ProcessorDependencies{
		Ledger: deps.Persistence, Assessor: deps.Assessor,
		Decisions: deps.Decisions, DecisionSettings: deps.DecisionSettings,
		EmbeddingExecutor: deps.EmbeddingExecutor, Limits: deps.Limits, Metrics: deps.Metrics,
		Logger: deps.Logger, DiagnosticProtector: deps.DiagnosticProtector, IsStaleInput: rememberapp.IsRememberStaleInputError,
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

func buildRememberInvocationDiagnostics(repo remembercontract.RememberInvocationDiagnosticsRepository) *rememberapp.RememberInvocationDiagnosticsService {
	return rememberapp.NewRememberInvocationDiagnosticsService(repo)
}
