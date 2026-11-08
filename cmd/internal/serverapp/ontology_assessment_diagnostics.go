package serverapp

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

func ontologyAssessmentDiagnostics(logger *slog.Logger, protector observability.DiagnosticProtector) assessment.Observer {
	if logger == nil {
		return nil
	}
	return func(ctx context.Context, diagnostic assessment.Diagnostic) {
		scope := observability.ExchangeScopeFromContext(ctx)
		if diagnostic.TeamID != "" {
			scope.TeamID = diagnostic.TeamID
		}
		if diagnostic.Request != nil {
			scope.AssessmentID = diagnostic.Request.RequestID
		}
		if diagnostic.Result != nil {
			scope.AssessmentID = diagnostic.Result.AssessmentID
		}
		attrs := []slog.Attr{slog.String("team_id", scope.TeamID), slog.String("run_id", scope.RunID),
			slog.String("batch_id", scope.BatchID), slog.String("operation_id", scope.OperationID),
			slog.Any("source_owner_ids", scope.SourceOwnerIDs),
			slog.String("assessment_id", scope.AssessmentID), slog.Int("attempt", diagnostic.Attempt),
			slog.String("stage", diagnostic.Stage), slog.String("policy_version", contract.OrganizationPolicyVersion),
			slog.String("schema", assessment.SchemaName), slog.String("provider_identity", diagnostic.ProviderIdentity),
			slog.String("model", diagnostic.Model), slog.Float64("elapsed_ms", float64(diagnostic.Duration)/float64(time.Millisecond))}
		captureState := "disabled"
		if logger.Enabled(ctx, slog.LevelDebug) {
			payload := map[string]any{"input": diagnostic.Input, "source_context": diagnostic.SourceContext,
				"source_dependencies": diagnostic.SourceDependencies, "request": diagnostic.Request, "response": diagnostic.Response,
				"result": diagnostic.Result, "policy": diagnostic.Policy, "limits": diagnostic.Limits,
				"context_window_tokens": diagnostic.ContextWindowTokens, "validation_errors": diagnostic.Validation}
			if diagnostic.Error != nil {
				payload["cause"] = diagnostic.Error.Error()
			}
			if diagnostic.SnapshotError != nil {
				payload["snapshot_error"] = diagnostic.SnapshotError.Error()
			}
			body, err := json.Marshal(payload)
			captureState = "captured"
			chunkCount := 0
			if err != nil {
				captureState = "formatting_failed"
			} else {
				for _, chunk := range observability.CaptureDiagnosticBody(ctx, protector, body, modelprovider.MaxProviderDiagnosticBodyBytes) {
					chunkCount++
					if chunk.State != "captured" {
						captureState = "unavailable"
					}
					logger.LogAttrs(observability.WithSinkSuppressed(ctx), slog.LevelDebug, "ontology_semantic_assessment",
						append(append([]slog.Attr{}, attrs...), slog.Any("payload_chunk", chunk))...)
				}
			}
			logger.LogAttrs(ctx, slog.LevelDebug, "ontology_semantic_assessment", append(append([]slog.Attr{}, attrs...),
				slog.String("capture_state", captureState), slog.Int("chunk_count", chunkCount))...)
			if captureState != "captured" {
				logger.LogAttrs(ctx, slog.LevelWarn, "ontology_semantic_assessment_capture_incomplete",
					append(append([]slog.Attr{}, attrs...), slog.String("capture_state", captureState))...)
			}
		}
		attrs = append(attrs, slog.String("capture_state", captureState))
		if diagnostic.Stage == "rejected" {
			logger.LogAttrs(ctx, slog.LevelWarn, "ontology_semantic_assessment_rejected", append(attrs,
				slog.String("reason", "response_invalid"), slog.Int("validation_error_count", len(diagnostic.Validation)),
				slog.String("transaction_outcome", "not_committed"))...)
		}
		if diagnostic.Stage != "completed" {
			return
		}
		if diagnostic.Result != nil {
			attrs = append(attrs, slog.Any("attempts", diagnostic.Result.Attempts), slog.Bool("existing", diagnostic.Result.Existing),
				slog.Bool("current", diagnostic.Result.Current), slog.String("failure_code", diagnostic.Result.FailureCode))
		}
		if diagnostic.Error != nil || diagnostic.Result == nil || diagnostic.Result.FailureCode != "" {
			if diagnostic.Result != nil && (diagnostic.Result.FreshnessRejection != nil || diagnostic.Result.FailureCode == "manager_override_conflict" || diagnostic.Result.FailureCode == "commit_conflict") {
				logger.LogAttrs(ctx, slog.LevelWarn, "ontology_semantic_assessment_rejected", append(append([]slog.Attr{}, attrs...),
					slog.String("reason", diagnostic.Result.FailureCode), slog.String("transaction_outcome", "not_committed"))...)
			}
			logger.LogAttrs(ctx, slog.LevelError, "ontology_semantic_assessment_failed", append(attrs, slog.String("transaction_outcome", "not_committed"))...)
			return
		}
		logger.LogAttrs(ctx, slog.LevelInfo, "ontology_semantic_assessment_completed", append(attrs, slog.String("transaction_outcome", "committed"))...)
	}
}
