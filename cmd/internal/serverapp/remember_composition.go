package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

type rememberApplicationDependencies struct {
	Ledger   *repository.LedgerRepositoryImpl
	Catalog  memoryservice.SubmissionAssessmentCatalog
	Assessor assessor.Provider
	Embedder embedding.EmbeddingProviderInterface
	Limits   assessor.SemanticAssessmentLimits
	Metrics  observability.DiscoverabilityMetrics
	Logger   observability.LogProvider
	Audit    securityRejectionAuditAppender
}

func buildRememberApplication(deps rememberApplicationDependencies) rememberapp.Service {
	processor := newRememberSynchronousProcessor(
		deps.Ledger,
		deps.Catalog,
		deps.Assessor,
		deps.Embedder,
		deps.Limits,
		deps.Metrics,
		deps.Logger,
	)
	return rememberapp.NewService(rememberapp.Dependencies{
		Synchronous: processor,
		Auditor:     newRememberSecurityRejectionAuditAdapter(deps.Audit),
		Metrics:     deps.Metrics,
		Logger:      deps.Logger,
	})
}
