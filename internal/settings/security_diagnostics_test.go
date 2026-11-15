package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/require"
)

func securityDiagnosticRequest() *domain.SecurityAuthFailureRequest {
	return &domain.SecurityAuthFailureRequest{
		ID: "snapshot-test", Method: "POST", URL: "/mcp", Route: "/mcp", Message: "invalid api key", AuthScheme: "bearer",
		Headers:       map[string][]string{"Authorization": {"[REDACTED]"}, "X-Request": {"diagnostic-header"}},
		Body:          "{\n  \"password\": \"ordinary content\", \"token\": \"request-token\", \"configured\": \"configured-secret\"\n}",
		HeaderCapture: domain.SecurityRequestCapture{State: "captured", Limit: domain.SecurityRequestHeaderLimit},
		BodyCapture:   domain.SecurityRequestCapture{State: "captured", Limit: domain.SecurityRequestBodyLimit},
		URLCapture:    domain.SecurityRequestCapture{State: "captured", Limit: 8192},
	}
}

func TestSecurityRequestProtectionAndRealLogs(t *testing.T) {
	for _, level := range []slog.Level{observability.LevelTrace, slog.LevelInfo} {
		t.Run(level.String(), func(t *testing.T) {
			repo := newFakeSecurityRepository()
			repo.settings.FailureThreshold = 1
			var output bytes.Buffer
			logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
			svc := NewSecurityServiceWithDiagnostics(repo, nil, SecurityDiagnosticsOptions{Protector: observability.NewCredentialProtector("configured-secret"), Logger: logger})
			ctx := observability.WithAuthenticationSecrets(context.Background(), "request-token")
			request := securityDiagnosticRequest()
			request.URL = "/mcp?credential=" + url.QueryEscape("request-token")
			ban, err := svc.RecordAuthFailure(ctx, "192.0.2.56", "mcp", "AUTH_INVALID", request)
			require.NoError(t, err)
			require.NotNil(t, ban)
			snapshot := securityRequestSnapshot(ban)
			require.NotNil(t, snapshot)
			require.Contains(t, snapshot.Body, "ordinary content")
			require.NotContains(t, snapshot.Body, "request-token")
			require.NotContains(t, snapshot.Body, "configured-secret")
			require.NotContains(t, snapshot.URL, "request-token")
			require.Equal(t, *ban.LastFailedAt, snapshot.CapturedAt)
			require.NotContains(t, output.String(), "request-token")
			require.NotContains(t, output.String(), "configured-secret")
			require.Contains(t, output.String(), "security.auto_ban")
			if level == slog.LevelInfo {
				require.NotContains(t, output.String(), "ordinary content")
				return
			}
			var payload strings.Builder
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var record struct {
					Msg          string                            `json:"msg"`
					SnapshotID   string                            `json:"snapshot_id"`
					PayloadChunk observability.DiagnosticBodyChunk `json:"payload_chunk"`
				}
				require.NoError(t, json.Unmarshal([]byte(line), &record))
				if record.Msg == "security.auth_failure.request" {
					require.Equal(t, request.ID, record.SnapshotID)
					payload.WriteString(record.PayloadChunk.Body)
				}
			}
			var reconstructed domain.SecurityAuthFailureRequest
			require.NoError(t, json.Unmarshal([]byte(payload.String()), &reconstructed))
			require.Equal(t, *snapshot, reconstructed)
			require.Equal(t, "invalid api key", reconstructed.Message)
			require.Equal(t, "AUTH_INVALID", reconstructed.Reason)
		})
	}
}

func TestSecurityRequestProtectionUnavailableAndStableBan(t *testing.T) {
	repo := newFakeSecurityRepository()
	repo.settings.FailureThreshold = 1
	var output bytes.Buffer
	logger := observability.NewWithHandler(slog.NewJSONHandler(&output, nil))
	svc := NewSecurityServiceWithDiagnostics(repo, nil, SecurityDiagnosticsOptions{Protector: observability.NewCredentialProtector(strings.Repeat("x", observability.MaxCredentialSecretBytes+1)), Logger: logger})
	ban, err := svc.RecordAuthFailure(context.Background(), "192.0.2.57", "mcp", "AUTH_INVALID", securityDiagnosticRequest())
	require.NoError(t, err)
	snapshot := securityRequestSnapshot(ban)
	require.Empty(t, snapshot.Body)
	require.Empty(t, snapshot.Headers)
	require.Equal(t, "unavailable", snapshot.BodyCapture.State)
	require.Contains(t, output.String(), "security.auth_failure.capture_unavailable")
	require.NotContains(t, output.String(), "ordinary content")
	replacement := securityDiagnosticRequest()
	replacement.ID = "later-blocked-request"
	again, err := svc.RecordAuthFailure(context.Background(), "192.0.2.57", "mcp", "AUTH_INVALID", replacement)
	require.NoError(t, err)
	require.Equal(t, snapshot.ID, securityRequestSnapshot(again).ID)
}

func TestSecurityRequestPreservesCompleteMaximumBodyAndEncodedSecrets(t *testing.T) {
	svc := NewSecurityServiceWithDiagnostics(newFakeSecurityRepository(), nil, SecurityDiagnosticsOptions{Protector: observability.NewCredentialProtector("a+protected/credential")})
	request := securityDiagnosticRequest()
	request.Body = strings.Repeat("\"", domain.SecurityRequestBodyLimit)
	protected := svc.protectSecurityRequest(context.Background(), request)
	require.Equal(t, request.Body, protected.Body)
	request.Body = `{"value":"a\u002bprotected\/credential"} ` + url.QueryEscape("a+protected/credential")
	protected = svc.protectSecurityRequest(context.Background(), request)
	require.Equal(t, "captured", protected.BodyCapture.State)
	require.NotContains(t, protected.Body, "protected")
	require.Contains(t, protected.Body, "[REDACTED]")
	request.CapturedAt = time.Now().UTC()
	unavailable := NewSecurityService(newFakeSecurityRepository(), nil).protectSecurityRequest(context.Background(), request)
	require.Equal(t, "protection_unavailable", unavailable.BodyCapture.Reason)
}
