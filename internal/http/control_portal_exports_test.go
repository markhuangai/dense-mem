package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/markhuangai/dense-mem/internal/audit"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/operations"
	"github.com/stretchr/testify/require"
)

func TestControlExportsRequireControlAuthorizationAndAreDisabledWithoutBindings(t *testing.T) {
	cfg := &config.Config{ControlPortalToken: "control-secret"}
	diagnostic := operations.NewDiagnosticService(operations.DiagnosticPresence{}, nil, nil, nil, operations.AuthorityBootstrap{})
	for _, enabled := range []bool{false, true} {
		bindings := ControlPortalBindings{}
		if enabled {
			bindings.Telemetry.AuditExport = audit.New(nil)
			bindings.Telemetry.Diagnostics = diagnostic
		}
		server, err := NewControlPortalServerWithCapabilityBindings(cfg, nil, nil, nil, bindings, HealthConfig{}, nil)
		require.NoError(t, err)
		for _, path := range []string{"/control/api/audit/export", "/control/api/diagnostics/bundle"} {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer ordinary-manager-credential")
			request.Header.Set("X-Role", "admin")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			require.Equal(t, http.StatusUnauthorized, response.Code)
		}
		request := httptest.NewRequest(http.MethodGet, "/control/api/diagnostics/bundle", nil)
		request.Header.Set("X-Control-Portal-Token", "control-secret")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if !enabled {
			require.Equal(t, http.StatusNotFound, response.Code)
			continue
		}
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), `"version":1`)
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		require.NotContains(t, response.Body.String(), "control-secret")
		for _, query := range []string{"limit=1001", "limit=-1", "limit=not-a-number", "team_id=invalid", "limit=1&limit=2", "cursor=invalid", "unknown=secret"} {
			request := httptest.NewRequest(http.MethodGet, "/control/api/audit/export?"+query, nil)
			request.Header.Set("X-Control-Portal-Token", "control-secret")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			expected := http.StatusUnprocessableEntity
			if query == "team_id=invalid" {
				expected = http.StatusBadRequest
			}
			require.Equal(t, expected, response.Code)
			require.Less(t, response.Body.Len(), 2048)
		}
		request = httptest.NewRequest(http.MethodGet, "/control/api/audit/export?team_id=00000000-0000-0000-0000-000000000001&limit=1", nil)
		request.Header.Set("X-Control-Portal-Token", "control-secret")
		response = httptest.NewRecorder()
		server.ServeHTTP(response, request)
		require.Equal(t, http.StatusServiceUnavailable, response.Code)
		require.NotContains(t, response.Body.String(), "checkpoint")
		request = httptest.NewRequest(http.MethodGet, "/control/api/diagnostics/bundle?unknown=secret", nil)
		request.Header.Set("X-Control-Portal-Token", "control-secret")
		response = httptest.NewRecorder()
		server.ServeHTTP(response, request)
		require.Equal(t, http.StatusUnprocessableEntity, response.Code)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request = httptest.NewRequest(http.MethodGet, "/control/api/diagnostics/bundle", nil).WithContext(ctx)
		request.Header.Set("X-Control-Portal-Token", "control-secret")
		response = httptest.NewRecorder()
		server.ServeHTTP(response, request)
		require.Equal(t, http.StatusServiceUnavailable, response.Code)
		require.NotContains(t, response.Body.String(), "context canceled")
	}
}
