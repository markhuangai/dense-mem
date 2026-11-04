package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/operations"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/stretchr/testify/require"
)

func TestProviderTestControlAPIUsesRealEmbeddingAndStrictAuthenticatedTargets(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer provider-operational-key", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"model":"fixture","data":[{"index":0,"embedding":[0.25,0.75]}]}`)
	}))
	defer upstream.Close()
	protector := observability.NewCredentialProtector("provider-operational-key", "control-test-token")
	client := observability.DiagnosticHTTPClient(upstream.Client(), nil, protector)
	provider := embedding.NewOpenAIEmbeddingProvider(&config.Config{AIAPIURL: upstream.URL, AIAPIKey: "provider-operational-key", AIEmbeddingModel: "fixture", AIEmbeddingDimensions: 2, AIEmbeddingTimeoutSeconds: 1}, client)
	service := operations.NewProviderTestService(func(context.Context) ([]operations.ProviderTestBinding, error) {
		return []operations.ProviderTestBinding{{Target: operations.ProviderTestTarget{Endpoint: upstream.URL + "/embeddings", Model: "fixture", Operation: "embed", Roles: []string{"Embeddings"}, Available: true, TimeoutMS: 1000}, Execute: func(ctx context.Context) (*modelprovider.CallDiagnostics, error) {
			_, _, err := provider.Embed(ctx, "Provider connectivity sample.")
			return nil, err
		}}}, nil
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)), protector, "http-fixture-server")
	portal, err := NewControlPortalServerWithCapabilityBindings(&config.Config{ControlPortalToken: "control-test-token"}, nil, nil, nil, ControlPortalBindings{Telemetry: ControlPortalTelemetry{ProviderTests: service}}, HealthConfig{}, nil)
	require.NoError(t, err)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if token != "" {
			r.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
		}
		response := httptest.NewRecorder()
		portal.ServeHTTP(response, r)
		return response
	}
	listed := request("GET", "/control/api/diagnostics/providers", "", "control-test-token")
	require.Equal(t, 200, listed.Code, listed.Body.String())
	var targets struct {
		Data []operations.ProviderTestTarget `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &targets))
	require.Len(t, targets.Data, 1)
	require.Zero(t, calls.Load())
	body := `{"target_id":"` + targets.Data[0].ID + `"}`
	for _, token := range []string{"", "wrong-control-token", "team-profile-memory-key"} {
		require.Equal(t, 401, request("POST", "/control/api/diagnostics/provider-tests", body, token).Code)
	}
	for _, invalid := range []string{`{}`, `{"target_id":"short"}`, strings.TrimSuffix(body, "}") + `,"url":"https://arbitrary.invalid"}`, body + ` {}`} {
		require.Equal(t, 422, request("POST", "/control/api/diagnostics/provider-tests", invalid, "control-test-token").Code)
	}
	require.Equal(t, 409, request("POST", "/control/api/diagnostics/provider-tests", `{"target_id":"`+strings.Repeat("f", 64)+`"}`, "control-test-token").Code)
	require.Zero(t, calls.Load())
	completed := request("POST", "/control/api/diagnostics/provider-tests", body, "control-test-token")
	require.Equal(t, 200, completed.Code, completed.Body.String())
	var result struct {
		Data operations.ProviderTestResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(completed.Body.Bytes(), &result))
	require.Equal(t, "succeeded", result.Data.Outcome)
	require.Equal(t, "http-fixture-server", result.Data.ServerInstance)
	require.NotEmpty(t, result.Data.CorrelationID)
	require.EqualValues(t, 1, calls.Load())
	require.NotContains(t, completed.Body.String(), "provider-operational-key")
}

func TestProviderTestControlAPIRequiresSSOCSRFBeforeExecution(t *testing.T) {
	providerID, identityID := uuid.New(), uuid.New()
	repository := newControlIdentityHTTPRepository()
	repository.groups = []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "admins", Enabled: true}}
	repository.sessions[accessservice.HashSSOToken("provider-test-session")] = &domain.ControlSession{SessionHash: accessservice.HashSSOToken("provider-test-session"), CSRFHash: accessservice.HashSSOToken("provider-test-csrf"), IdentityID: identityID, ProviderID: providerID, GroupIDs: []string{"admins"}, ExpiresAt: time.Now().Add(time.Hour)}
	sso := &controlIdentityHTTPSSORepository{providers: map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: true}}, identities: map[uuid.UUID]*domain.SSOIdentity{identityID: {ID: identityID, ProviderID: providerID, Active: true}}}
	identity := accessservice.NewControlIdentityService(repository, sso, accessservice.ControlIdentityConfig{})
	portal, err := NewControlPortalServerWithCapabilityBindings(&config.Config{ControlPortalToken: "static-control"}, nil, nil, nil, ControlPortalBindings{Telemetry: ControlPortalTelemetry{ControlIdentity: identity, ProviderTests: operations.NewProviderTestService(func(context.Context) ([]operations.ProviderTestBinding, error) {
		return []operations.ProviderTestBinding{}, nil
	}, nil, observability.NewCredentialProtector(), "server")}}, HealthConfig{}, nil)
	require.NoError(t, err)
	for _, csrf := range []string{"", "wrong", "provider-test-csrf"} {
		request := httptest.NewRequest("POST", "/control/api/diagnostics/provider-tests", strings.NewReader(`{"target_id":"`+strings.Repeat("a", 64)+`"}`))
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		request.AddCookie(&nethttp.Cookie{Name: accessservice.ControlSessionCookieName, Value: "provider-test-session"})
		request.Header.Set(accessservice.ControlCSRFHeaderName, csrf)
		response := httptest.NewRecorder()
		portal.ServeHTTP(response, request)
		expected := 401
		if csrf == "provider-test-csrf" {
			expected = 409
		}
		require.Equal(t, expected, response.Code, response.Body.String())
	}
}
