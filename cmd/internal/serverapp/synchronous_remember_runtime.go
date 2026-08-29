package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/synchronousremember"
)

func installSynchronousRememberFactory(
	writeRuntime *WriteRuntime,
	ledger synchronousremember.SynchronousRememberLedger,
	catalog memoryservice.SubmissionAssessmentCatalog,
	provider assessor.Provider,
	limits assessor.SemanticAssessmentLimits,
	embeddingProvider embedding.EmbeddingProviderInterface,
	intake rememberapp.IntakePort,
	auditor rememberapp.SecurityRejectionAuditor,
	metrics observability.DiscoverabilityMetrics,
	logger observability.LogProvider,
) {
	writeRuntime.SynchronousRememberFactory = func() rememberapp.Service {
		processor := synchronousremember.NewSynchronousRememberProcessor(synchronousremember.SynchronousRememberProcessorDependencies{
			Ledger: ledger, Catalog: catalog, Provider: provider, Limits: limits,
			Embeddings: newSemanticwriteEmbeddingExecutor(embeddingProvider), Auditor: auditor, Metrics: metrics, Logger: logger,
			BeforeCommit: writeRuntime.SynchronousRememberBeforeCommit,
		})
		return rememberapp.NewService(rememberapp.Dependencies{
			Intake: intake, Synchronous: processor, Auditor: auditor,
			Metrics: metrics, Logger: logger,
		})
	}
}
