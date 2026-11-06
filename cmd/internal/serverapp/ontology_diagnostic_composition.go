package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	remember "github.com/markhuangai/dense-mem/internal/remember/service/processor"
)

func ontologyDiagnosticCapture(sink observability.LogSink, protector observability.DiagnosticProtector, logger *slog.Logger) ontology.MaintenanceDiagnosticCapture {
	return ontology.MaintenanceDiagnosticCapture{
		NewRecorder: func() modelprovider.SnapshotRecorder { return remember.NewDiagnosticRecorder(protector) },
		RecordFailure: func(ctx context.Context, claim contract.MaintenanceClaim, result contract.OrganizationResult, exchanges []modelprovider.ProviderExchange) error {
			if sink == nil || protector == nil {
				return errors.New("ontology diagnostic sink unavailable")
			}
			if _, err := uuid.Parse(claim.TeamID); err != nil {
				return err
			}
			captureID := uuid.NewString()
			base := map[string]any{"reference_type": "ontology_run", "reference_id": claim.RunID, "run_id": claim.RunID, "batch_id": claim.ID, "assessment_id": result.AssessmentID, "attempt_id": captureID}
			write := func(event string, attrs map[string]any, chunk *observability.DiagnosticBodyChunk) error {
				if chunk != nil {
					metadata := *chunk
					metadata.Body = ""
					attrs["payload_chunk"] = metadata
				}
				for key, value := range base {
					attrs[key] = value
				}
				body, err := json.Marshal(attrs)
				if err != nil {
					return err
				}
				body, reason := protector.ProtectDiagnosticBytes(body, observability.MaxOperationMetadataBytes, observability.AuthenticationSecretsFromContext(ctx)...)
				if reason != observability.CredentialProtectionAvailable {
					return errors.New("ontology diagnostic metadata unavailable")
				}
				var protected map[string]any
				if err := json.Unmarshal(body, &protected); err != nil {
					return err
				}
				if chunk != nil {
					metadata, ok := protected["payload_chunk"].(map[string]any)
					if !ok || metadata["body"] != "" {
						return errors.New("ontology diagnostic chunk metadata unavailable")
					}
					// Complete bodies are protected before splitting so credential matches cannot cross chunk boundaries.
					metadata["body"] = chunk.Body
					body, err = json.Marshal(protected)
					if err != nil || len(body) > observability.MaxOperationMetadataBytes {
						return errors.New("ontology diagnostic metadata exceeds record budget")
					}
				}
				return sink.WriteLog(ctx, observability.LogRecord{Timestamp: time.Now().UTC(), Severity: "DEBUG", Message: event, TeamID: claim.TeamID, Attrs: protected, Contextual: true})
			}
			records := 0
			for index, exchange := range exchanges {
				for _, direction := range []string{"request", "response"} {
					body := exchange.RequestBody
					if direction == "response" {
						body = exchange.ResponseBody
					}
					for _, chunk := range observability.ChunkProtectedDiagnosticBody(body, modelprovider.MaxProviderDiagnosticBodyBytes) {
						attrs := map[string]any{"exchange": index, "direction": direction, "component": exchange.Component, "model": exchange.Model,
							"status_code": exchange.StatusCode, "outcome": exchange.Outcome, "capture_state": exchange.CaptureState, "capture_reason": exchange.CaptureReason}
						if err := write("ontology_failure_payload", attrs, &chunk); err != nil {
							return err
						}
						records++
					}
				}
			}
			manifest := map[string]any{"failure_code": result.FailureCode, "exchange_count": len(exchanges), "record_count": records, "attempts": result.Attempts}
			if result.FreshnessRejection != nil {
				if !result.FreshnessRejection.Valid() {
					return errors.New("ontology freshness diagnostics invalid")
				}
				body, err := json.Marshal(result.FreshnessRejection)
				if err != nil {
					return err
				}
				chunks := observability.CaptureDiagnosticBody(ctx, protector, body, contract.MaxPublicationBytes)
				for _, chunk := range chunks {
					if err := write("ontology_freshness_payload", map[string]any{"check": result.FreshnessRejection.Check}, &chunk); err != nil {
						return err
					}
					if logger != nil {
						logger.LogAttrs(observability.WithSinkSuppressed(ctx), slog.LevelDebug, "ontology_freshness_payload",
							slog.String("team_id", claim.TeamID), slog.String("run_id", claim.RunID), slog.String("batch_id", claim.ID),
							slog.String("assessment_id", result.AssessmentID), slog.Any("payload_chunk", chunk))
					}
				}
				summary := *result.FreshnessRejection
				summary.ExpectedContext, summary.ObservedContext, summary.Dependencies = nil, nil, nil
				manifest["freshness_rejection"], manifest["freshness_record_count"] = summary, len(chunks)
			}
			return write("ontology_failure_capture", manifest, nil)
		},
	}
}
