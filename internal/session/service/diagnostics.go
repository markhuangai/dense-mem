package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
)

func (s *Service) recordDiagnostics(ctx context.Context, submission *session.Submission, request session.Request, result *session.Result, recorder modelprovider.SnapshotRecorder) {
	if s.deps.DiagnosticProtector == nil {
		s.diagnosticUnavailable("credential_protection_unavailable")
		return
	}
	exchanges := []modelprovider.ProviderExchange{}
	if recorder != nil {
		exchanges = recorder.Snapshot()
	}
	for index := range exchanges {
		exchanges[index].ResponseBodyProjection = nil
	}
	payload, err := json.Marshal(map[string]any{"tool": "ingest_session", "request": request, "result": result, "exchanges": exchanges})
	if err != nil {
		s.diagnosticUnavailable("serialization_failed")
		return
	}
	protected, reason := s.deps.DiagnosticProtector.ProtectDiagnosticBytes(payload, 64<<20, observability.AuthenticationSecretsFromContext(ctx)...)
	if reason != observability.CredentialProtectionAvailable {
		s.diagnosticUnavailable("credential_protection_" + fmt.Sprint(int(reason)))
		return
	}
	capture := "captured"
	for _, exchange := range exchanges {
		if exchange.CaptureState == "unavailable" {
			capture = "unavailable"
			break
		}
		if exchange.CaptureState == "truncated" {
			capture = "truncated"
		}
	}
	body, err := json.Marshal(map[string]any{"capture_state": capture, "payload": json.RawMessage(protected)})
	if err != nil {
		s.diagnosticUnavailable("serialization_failed")
		return
	}
	persistenceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = s.deps.Repository.RecordSessionDiagnostic(persistenceCtx, submission.Intake.Scope, submission.ID, body)
	cancel()
	if err != nil {
		s.diagnosticUnavailable("persistence_failed")
	}
}

func (s *Service) diagnosticUnavailable(reason string) {
	if s.deps.Logger != nil {
		s.deps.Logger.Warn("session_diagnostics_unavailable", observability.String("reason_code", reason))
	}
}
