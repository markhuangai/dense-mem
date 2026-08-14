package http

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type identityCleanupPreflightStub struct {
	report domain.IdentityCleanupPreflight
	err    error
}

func (s identityCleanupPreflightStub) Preflight(context.Context) (domain.IdentityCleanupPreflight, error) {
	return s.report, s.err
}

func TestControlPortalIdentityCleanupPreflightIsReadOnlyAndBounded(t *testing.T) {
	server, err := NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "token"},
		&controlProfileSvc{}, &controlKeySvc{}, nil,
		ControlPortalTelemetry{IdentityCleanup: identityCleanupPreflightStub{report: domain.IdentityCleanupPreflight{
			Ready:    false,
			Blockers: []domain.IdentityCleanupBlocker{{Code: "backup_checkpoint_missing", Message: "a verified recovery checkpoint is required before cleanup"}},
		}}},
		HealthConfig{}, nil,
	)
	require.NoError(t, err)
	response := identityCleanupRequest(t, server, "GET", "/control/api/identity-cleanup/preflight", "token")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), "backup_checkpoint_missing")
	require.NotContains(t, response.Body.String(), "team_profiles")
}

func TestControlPortalIdentityCleanupPreflightMapsUnavailable(t *testing.T) {
	server, err := NewControlPortalServerWithMetricsAndTelemetry(
		&config.Config{ControlPortalToken: "token"},
		&controlProfileSvc{}, &controlKeySvc{}, nil,
		ControlPortalTelemetry{IdentityCleanup: identityCleanupPreflightStub{err: service.ErrIdentityCleanupPreflightUnavailable}},
		HealthConfig{}, nil,
	)
	require.NoError(t, err)
	response := identityCleanupRequest(t, server, "GET", "/control/api/identity-cleanup/preflight", "token")
	require.Equal(t, 503, response.Code)
}

func identityCleanupRequest(t *testing.T, server *echo.Echo, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}
