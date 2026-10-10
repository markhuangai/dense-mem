package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	remember "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	sessionservice "github.com/markhuangai/dense-mem/internal/session/service"
)

func buildSessionApplication(deps rememberApplicationDependencies, repository session.Repository, transport modelprovider.StructuredTransport, model string, enabled bool) session.API {
	limits := assessor.NormalizeSemanticAssessmentLimits(deps.Limits)
	limits.MaxEvidenceItems = session.MaxExcerpts
	preparer := remember.NewSynchronousProcessor(remember.ProcessorDependencies{
		Ledger: deps.Persistence, Catalog: deps.Catalog, Assessor: deps.Assessor, Embedder: deps.Embedder,
		Limits: limits, Metrics: deps.Metrics, Logger: deps.Logger, DiagnosticProtector: deps.DiagnosticProtector,
	})
	return sessionservice.NewService(sessionservice.Dependencies{
		Repository: repository, Preparer: preparer, Extractor: extraction.NewProvider(transport, model, limits),
		Enabled: enabled, Tokenizer: limits.Tokenizer, Auditor: newRememberSecurityRejectionAuditAdapter(deps.Audit), Logger: deps.Logger,
		DiagnosticProtector: deps.DiagnosticProtector,
		DiagnosticRecorder:  func() modelprovider.SnapshotRecorder { return remember.NewDiagnosticRecorder(deps.DiagnosticProtector) },
	})
}
