package settings

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const securitySnapshotEncodingLimit = 1 << 20

func (s *SecurityServiceImpl) protectSecurityRequest(ctx context.Context, request *domain.SecurityAuthFailureRequest) *domain.SecurityAuthFailureRequest {
	reason := "protection_unavailable"
	if s.diagnostics.Protector != nil {
		encoded, err := json.Marshal(request)
		if err == nil {
			protected, unavailable := s.diagnostics.Protector.ProtectDiagnosticBytes(encoded, securitySnapshotEncodingLimit, observability.AuthenticationSecretsFromContext(ctx)...)
			if unavailable == observability.CredentialProtectionAvailable {
				var snapshot domain.SecurityAuthFailureRequest
				if json.Unmarshal(protected, &snapshot) == nil && snapshot.ID == request.ID && snapshot.HeaderCapture.State == request.HeaderCapture.State && snapshot.BodyCapture.State == request.BodyCapture.State && snapshot.URLCapture.State == request.URLCapture.State {
					return &snapshot
				}
				reason = "protection_invalid_result"
			} else {
				reason = unavailable.Code()
			}
		} else {
			reason = "protection_encoding_failed"
		}
	}
	return &domain.SecurityAuthFailureRequest{
		ID: request.ID, CapturedAt: request.CapturedAt,
		HeaderCapture: domain.SecurityRequestCapture{State: "unavailable", Reason: reason, Limit: domain.SecurityRequestHeaderLimit},
		BodyCapture:   domain.SecurityRequestCapture{State: "unavailable", Reason: reason, Limit: domain.SecurityRequestBodyLimit},
		URLCapture:    domain.SecurityRequestCapture{State: "unavailable", Reason: reason, Limit: 8192},
	}
}

func securityRequestSnapshot(ban *domain.SecurityIPBan) *domain.SecurityAuthFailureRequest {
	if ban == nil || ban.Metadata["last_request"] == nil {
		return nil
	}
	encoded, err := json.Marshal(ban.Metadata["last_request"])
	if err != nil {
		return nil
	}
	var request domain.SecurityAuthFailureRequest
	if json.Unmarshal(encoded, &request) != nil {
		return nil
	}
	return &request
}

func sameSecurityRequest(ban *domain.SecurityIPBan, request *domain.SecurityAuthFailureRequest) bool {
	if ban.Source != domain.SecurityBanSourceAuto {
		return false
	}
	if request == nil {
		return true
	}
	snapshot := securityRequestSnapshot(ban)
	return snapshot != nil && snapshot.ID == request.ID
}

func (s *SecurityServiceImpl) securityDiagnosticLog(ctx context.Context, level, event string, err error, ban *domain.SecurityIPBan, started time.Time) {
	logger := s.diagnostics.Logger
	if logger == nil {
		return
	}
	attrs := []observability.LogAttr{
		observability.String("event", event), observability.ClientIP(ban.IP),
		observability.String("reason", ban.Reason), observability.Int("failure_count", ban.FailureCount),
		{Key: "settings", Value: map[string]any{"failure_threshold": ban.Metadata["failure_threshold"], "failure_window_seconds": ban.Metadata["failure_window_seconds"], "ban_duration_seconds": ban.Metadata["ban_duration_seconds"], "expires_at": ban.ExpiresAt}},
		observability.Int("duration_ms", int(time.Since(started).Milliseconds())),
	}
	if request := securityRequestSnapshot(ban); request != nil {
		attrs = append(attrs, observability.String("snapshot_id", request.ID),
			observability.String("message", request.Message), observability.String("auth_scheme", request.AuthScheme),
			observability.String("credential_fingerprint", request.CredentialFingerprint),
			observability.String("credential_prefix", request.CredentialPrefix),
			observability.String("body_capture_state", request.BodyCapture.State), observability.String("body_capture_reason", request.BodyCapture.Reason),
			observability.String("header_capture_state", request.HeaderCapture.State), observability.String("header_capture_reason", request.HeaderCapture.Reason))
	}
	severity := slog.LevelInfo
	if level == "ERROR" {
		severity = slog.LevelError
	}
	if level == "WARN" {
		severity = slog.LevelWarn
	}
	if contextual, ok := logger.(observability.ContextLogProvider); ok {
		if err != nil {
			attrs = append(attrs, observability.String("error", err.Error()))
		}
		contextual.LogContext(ctx, severity, event, attrs...)
	} else {
		switch level {
		case "ERROR":
			logger.Error(event, err, attrs...)
		case "WARN":
			logger.Warn(event, attrs...)
		default:
			logger.Info(event, attrs...)
		}
	}
}

func (s *SecurityServiceImpl) logSecurityRequest(ctx context.Context, ban *domain.SecurityIPBan, started time.Time) {
	s.securityDiagnosticLog(ctx, "INFO", "security.auto_ban", nil, ban, started)
	request := securityRequestSnapshot(ban)
	if request == nil {
		return
	}
	if request.HeaderCapture.State == "unavailable" || request.BodyCapture.State == "unavailable" || request.URLCapture.State == "unavailable" {
		s.securityDiagnosticLog(ctx, "WARN", "security.auth_failure.capture_unavailable", nil, ban, started)
	}
	s.logSecurityRequestPayload(ctx, ban)
}

func (s *SecurityServiceImpl) logSecurityRequestPayload(ctx context.Context, ban *domain.SecurityIPBan) {
	request := securityRequestSnapshot(ban)
	if request == nil {
		return
	}
	logger, ok := s.diagnostics.Logger.(observability.ContextLogProvider)
	if !ok {
		return
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return
	}
	for _, chunk := range observability.ChunkProtectedDiagnosticBody(encoded, securitySnapshotEncodingLimit) {
		logger.DebugContext(observability.WithSinkSuppressed(ctx), "security.auth_failure.request",
			observability.String("event", "security.auth_failure.request"), observability.String("snapshot_id", request.ID),
			observability.ClientIP(ban.IP), observability.LogAttr{Key: "payload_chunk", Value: chunk})
	}
}
