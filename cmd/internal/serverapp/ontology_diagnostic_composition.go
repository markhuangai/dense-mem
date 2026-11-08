package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ontology"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	remember "github.com/markhuangai/dense-mem/internal/remember/service/processor"
)

func ontologyPublicationDiagnostics(logger *slog.Logger, protector observability.DiagnosticProtector) contract.PublicationObserver {
	if logger == nil {
		return nil
	}
	return func(ctx context.Context, diagnostic contract.PublicationDiagnostic) {
		base := []slog.Attr{slog.String("team_id", diagnostic.TeamID), slog.String("operation_id", diagnostic.OperationID),
			slog.String("action", diagnostic.Action), slog.String("fingerprint_version", contract.FingerprintVersion),
			slog.Float64("elapsed_ms", float64(diagnostic.Duration)/float64(time.Millisecond))}
		if claim, ok := contract.MaintenanceClaimFromContext(ctx); ok {
			base = append(base, slog.String("run_id", claim.RunID), slog.String("batch_id", claim.ID), slog.String("window_id", claim.WindowID),
				slog.String("shared_space_id", claim.SpaceID), slog.Int64("space_generation", claim.Generation))
		}
		if errors.Is(diagnostic.Failure, contract.ErrUnauthorized) || (diagnostic.Management && errors.Is(diagnostic.Failure, contract.ErrNotFound)) {
			event, reason := "ontology_semantic_publication_rejected", "unauthorized"
			if diagnostic.Management {
				event = "ontology_semantic_manager_rejected"
			}
			if errors.Is(diagnostic.Failure, contract.ErrNotFound) {
				reason = "unavailable"
			}
			logger.LogAttrs(ctx, slog.LevelWarn, event, append(base, slog.String("reason", reason), slog.String("transaction_outcome", "not_committed"))...)
			return
		}
		var input struct {
			OperationKey     string `json:"operation_key"`
			ExpectedRevision *int64 `json:"expected_revision"`
			ExpectedVersion  *int64 `json:"expected_version"`
		}
		if diagnostic.InputState == "captured" && json.Unmarshal(diagnostic.Input, &input) == nil {
			if input.OperationKey != "" {
				base = append(base, slog.String("operation_key", input.OperationKey))
			}
			if input.ExpectedRevision != nil {
				base = append(base, slog.Int64("expected_revision", *input.ExpectedRevision))
			}
			if input.ExpectedVersion != nil {
				base = append(base, slog.Int64("expected_version", *input.ExpectedVersion))
			}
		}
		if diagnostic.DraftID != "" {
			base = append(base, slog.String("draft_id", diagnostic.DraftID))
		}
		if diagnostic.RollbackOf != "" {
			base = append(base, slog.String("target_publication_id", diagnostic.RollbackOf))
		}
		if diagnostic.Draft != nil {
			base = append(base, slog.Int64("draft_version", diagnostic.Draft.Version), slog.Int64("draft_revision", diagnostic.Draft.BaseRevision), slog.String("draft_state", diagnostic.Draft.State))
		}
		if diagnostic.ObservedRevision != nil {
			base = append(base, slog.Int64("observed_revision", *diagnostic.ObservedRevision))
		}
		if diagnostic.AssessmentID != "" {
			base = append(base, slog.String("assessment_id", diagnostic.AssessmentID))
		}
		if diagnostic.ProviderIdentity != "" {
			base = append(base, slog.String("provider_identity", diagnostic.ProviderIdentity))
		}
		reason := "accepted"
		level, event := slog.LevelInfo, "ontology_semantic_publication_committed"
		if diagnostic.Failure != nil {
			level, event, reason = slog.LevelError, "ontology_semantic_publication_failed", "storage_failure"
			if errors.Is(diagnostic.Failure, contract.ErrInvalid) || errors.Is(diagnostic.Failure, contract.ErrConflict) || errors.Is(diagnostic.Failure, contract.ErrSourceStale) || errors.Is(diagnostic.Failure, contract.ErrOverride) {
				level, event, reason = slog.LevelWarn, "ontology_semantic_publication_rejected", "invalid_publication"
			}
			if diagnostic.Rejection != nil {
				reason = diagnostic.Rejection.Reason
			}
		}
		if diagnostic.Management {
			if diagnostic.Failure == nil {
				event = "ontology_semantic_manager_command"
				if diagnostic.Preview != nil {
					level, event = slog.LevelDebug, "ontology_semantic_draft_preview"
				}
			} else if level == slog.LevelWarn {
				event = "ontology_semantic_manager_rejected"
			}
		}
		base = append(base, slog.String("reason", reason))
		captureState := "disabled"
		if logger.Enabled(ctx, slog.LevelDebug) {
			captureState = "captured"
			payloads, chunks := 0, 0
			emit := func(kind, id string, body []byte) {
				payloads++
				for _, chunk := range observability.CaptureDiagnosticBody(ctx, protector, body, contract.MaxPublicationBytes) {
					chunks++
					if chunk.State != "captured" {
						captureState = "unavailable"
					}
					attrs := append(append([]slog.Attr{}, base...), slog.String("payload_kind", kind), slog.String("record_id", id), slog.Any("payload_chunk", chunk))
					logger.LogAttrs(observability.WithSinkSuppressed(ctx), slog.LevelDebug, "ontology_semantic_publication", attrs...)
				}
			}
			if diagnostic.InputState == "captured" {
				emit("input", "", diagnostic.Input)
			} else {
				captureState = "unavailable"
				logger.LogAttrs(ctx, slog.LevelWarn, "diagnostic_body_unavailable", append(append([]slog.Attr{}, base...),
					slog.String("capture_event", "ontology_semantic_publication"), slog.String("reason_code", diagnostic.InputReason),
					slog.Int("original_bytes", diagnostic.InputBytes), slog.Int("limit", contract.MaxPublicationBytes), slog.String("limit_unit", "body_bytes"))...)
			}
			marshal := func(kind, id string, value any) {
				body, err := json.Marshal(value)
				if err != nil {
					captureState = "unavailable"
					logger.LogAttrs(ctx, slog.LevelWarn, "diagnostic_body_unavailable", append(append([]slog.Attr{}, base...), slog.String("reason_code", "formatting_failed"), slog.String("payload_kind", kind))...)
					return
				}
				emit(kind, id, body)
			}
			if diagnostic.Publication != nil {
				marshal("publication", "", diagnostic.Publication)
			}
			if diagnostic.Draft != nil {
				marshal("draft", "", diagnostic.Draft)
			}
			if diagnostic.Preview != nil {
				marshal("preview", "", diagnostic.Preview)
			}
			ids := make([]string, 0, len(diagnostic.Definitions))
			for id := range diagnostic.Definitions {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				marshal("definition", id, diagnostic.Definitions[id])
			}
			if diagnostic.Rejection != nil {
				marshal("rejection", "", diagnostic.Rejection)
			}
			if diagnostic.DefinitionsState != "" {
				captureState = diagnostic.DefinitionsState
			}
			logger.LogAttrs(ctx, slog.LevelDebug, "ontology_semantic_publication", append(append([]slog.Attr{}, base...),
				slog.String("capture_state", captureState), slog.Int("payload_count", payloads), slog.Int("chunk_count", chunks), slog.Int("definition_count", len(ids)))...)
		}
		if captureState != "captured" && captureState != "disabled" {
			logger.LogAttrs(ctx, slog.LevelWarn, "ontology_semantic_publication_capture_incomplete", append(append([]slog.Attr{}, base...), slog.String("capture_state", captureState))...)
		}
		base = append(base, slog.String("capture_state", captureState))
		if diagnostic.Failure != nil {
			logger.LogAttrs(ctx, level, event, append(base, slog.String("transaction_outcome", "not_committed"), slog.String("error", diagnostic.Failure.Error()))...)
		} else {
			if diagnostic.Result != nil && diagnostic.Result.ID != "" {
				base = append(base, slog.String("publication_id", diagnostic.Result.ID), slog.Int64("revision", diagnostic.Result.Revision), slog.Bool("existing", diagnostic.Result.Existing), slog.Any("revisions", diagnostic.Result.Records))
			} else if diagnostic.Draft == nil && diagnostic.Preview == nil {
				return
			}
			outcome := "committed"
			if preview := diagnostic.Preview; preview != nil {
				outcome = "read_only"
				base = append(base, slog.Int("changed_records", preview.ChangedRecords), slog.Int64("affected_records", preview.AffectedRecords), slog.Int64("affected_sources", preview.AffectedSources), slog.Bool("complete", preview.Complete))
			}
			logger.LogAttrs(ctx, level, event, append(base, slog.String("transaction_outcome", outcome))...)
		}
	}
}

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
