package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	cryptoutil "github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
	"github.com/stretchr/testify/require"
)

type securitySnapshotRepository struct {
	settingscontract.SecurityRepository
	ban *domain.SecurityIPBan
}

func (r *securitySnapshotRepository) GetSettings(context.Context) (*domain.SecuritySettings, error) {
	return &domain.SecuritySettings{Enabled: true, FailureThreshold: 1, FailureWindowSeconds: 600}, nil
}

func (r *securitySnapshotRepository) GetActiveBan(context.Context, string, time.Time) (*domain.SecurityIPBan, error) {
	return r.ban, nil
}
func (r *securitySnapshotRepository) RecordFailure(_ context.Context, ip, surface, reason string, _ int, at time.Time) (*domain.SecurityIPFailure, error) {
	return &domain.SecurityIPFailure{IP: ip, FailureCount: 1, LastFailedAt: at, LastReason: reason, LastSurface: surface}, nil
}
func (r *securitySnapshotRepository) UpsertBan(_ context.Context, ban *domain.SecurityIPBan) error {
	r.ban = ban
	return nil
}

func TestDirectorySCIMOAuthFailureRequestDiagnostics(t *testing.T) {
	const valid = "valid-client-credential"
	const presented = "wrong+client/credential"
	hash, err := cryptoutil.HashKey(valid)
	require.NoError(t, err)
	for _, mode := range []string{"basic", "form", "query", "success"} {
		t.Run(mode, func(t *testing.T) {
			directoryRepo := &directorySCIMRepositoryStub{connector: &domain.DirectoryConnector{ID: uuid.New(), Status: domain.DirectoryConnectorObserve, OAuthClientID: "client", OAuthClientSecretHash: hash}, oauthTokens: make(map[string]directorySCIMOAuthToken)}
			directory := accessservice.NewDirectoryIdentityService(directoryRepo, accessservice.DirectoryIdentityConfig{})
			securityRepo := &securitySnapshotRepository{}
			var output bytes.Buffer
			logger := observability.NewWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: observability.LevelTrace}))
			security := settings.NewSecurityServiceWithDiagnostics(securityRepo, nil, settings.SecurityDiagnosticsOptions{Protector: observability.NewCredentialProtector("configured-diagnostic-secret"), Logger: logger})
			h := &directorySCIMHandler{directory: directory, security: security}
			body := "grant_type=client_credentials&marker=original%2Bwire"
			path := "/scim/oauth/token"
			if mode == "form" {
				body += "&client_id=client&client_secret=" + url.QueryEscape(presented)
			}
			if mode == "query" {
				path += "?client_id=client&client_secret=" + url.QueryEscape(presented)
			}
			if mode == "success" {
				body += "&client_id=client&client_secret=" + url.QueryEscape(valid)
			}
			req := httptest.NewRequest(nethttp.MethodPost, path, strings.NewReader(body))
			req.RemoteAddr = "192.0.2.128:1234"
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if mode == "basic" {
				req.SetBasicAuth("client", presented)
			}
			ctx := echo.New().NewContext(req, httptest.NewRecorder())
			require.NoError(t, h.oauthToken(ctx))
			if mode == "success" {
				require.Equal(t, nethttp.StatusOK, ctx.Response().Status)
				require.Equal(t, "original+wire", ctx.Request().PostForm.Get("marker"))
				require.Nil(t, securityRepo.ban)
				return
			}
			require.Equal(t, nethttp.StatusUnauthorized, ctx.Response().Status)
			require.NotNil(t, securityRepo.ban)
			encoded, err := json.Marshal(securityRepo.ban.Metadata["last_request"])
			require.NoError(t, err)
			var snapshot domain.SecurityAuthFailureRequest
			require.NoError(t, json.Unmarshal(encoded, &snapshot))
			require.Contains(t, snapshot.Body, "marker=original%2Bwire")
			require.NotContains(t, string(encoded), presented)
			require.NotContains(t, string(encoded), url.QueryEscape(presented))
			require.NotContains(t, output.String(), presented)
			require.NotContains(t, output.String(), url.QueryEscape(presented))
			require.Contains(t, output.String(), "security.auth_failure.request")
			if mode == "basic" {
				require.Equal(t, "basic", snapshot.AuthScheme)
			} else {
				require.Equal(t, "client_secret", snapshot.AuthScheme)
			}
			require.Len(t, snapshot.CredentialFingerprint, 64)
		})
	}
}
