package serverapp

import (
	"context"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	remember "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	sessionservice "github.com/markhuangai/dense-mem/internal/session/service"
	"log/slog"
)

func buildSessionApplication(deps rememberApplicationDependencies, repository session.Repository, transport modelprovider.StructuredTransport, model string, enabled bool) session.API {
	limits := assessor.NormalizeSemanticAssessmentLimits(deps.Limits)
	limits.MaxEvidenceItems = session.MaxExcerpts
	preparer := remember.NewSynchronousProcessor(remember.ProcessorDependencies{
		Ledger: deps.Persistence, Assessor: deps.Assessor, EmbeddingExecutor: deps.EmbeddingExecutor,
		Decisions: deps.Decisions, DecisionSettings: deps.DecisionSettings,
		Limits: limits, Metrics: deps.Metrics, Logger: deps.Logger, DiagnosticProtector: deps.DiagnosticProtector,
	})
	return sessionservice.NewService(sessionservice.Dependencies{
		Repository: repository, Preparer: preparer, Extractor: extraction.NewProvider(transport, model, limits),
		Enabled: enabled, Tokenizer: limits.Tokenizer, Auditor: newRememberSecurityRejectionAuditAdapter(deps.Audit), Logger: deps.Logger,
		DiagnosticProtector: deps.DiagnosticProtector,
		DiagnosticRecorder:  func() modelprovider.SnapshotRecorder { return remember.NewDiagnosticRecorder(deps.DiagnosticProtector) },
		DiagnosticLog: func(ctx context.Context, scope session.Scope, id string, body []byte) {
			observability.LogDiagnosticBody(ctx, rootDiagnosticLogger(deps.Logger), deps.DiagnosticProtector,
				"session_evidence_diagnostics", body, 64<<20, slog.String("team_id", scope.TeamID),
				slog.String("owner_profile_id", scope.OwnerProfileID), slog.String("submission_id", id),
				slog.String("space_id", scope.SpaceID), slog.Int64("space_generation", scope.SpaceGeneration))
		},
	})
}
