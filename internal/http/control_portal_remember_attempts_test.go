package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestControlPortalRememberAttemptRoutes(t *testing.T) {
	teamID, attemptID := uuid.New(), uuid.New()
	reader := &controlRememberAttemptDiagnosticsStub{
		page: &rememberapp.RememberAttemptDiagnosticPage{Items: []rememberapp.RememberAttemptDiagnosticSummary{{TeamID: teamID.String(), AttemptID: attemptID.String(), Outcome: "failed", CreatedAt: time.Now().UTC()}}, Total: 1},
		detail: &rememberapp.RememberAttemptDiagnosticDetail{
			RememberAttemptDiagnosticSummary: rememberapp.RememberAttemptDiagnosticSummary{TeamID: teamID.String(), AttemptID: attemptID.String(), Outcome: "failed"},
			Events:                           []rememberapp.RememberAttemptDiagnosticEvent{},
			Diagnostics: rememberapp.RememberAttemptDiagnostics{
				OriginalRequest:   &rememberapp.RememberDiagnosticExchange{Kind: "original_request", RequestBody: `{"evidence":[]}`, Outcome: "captured"},
				ProviderExchanges: []rememberapp.RememberDiagnosticExchange{{Kind: "provider_exchange", Component: "assessor", RequestBody: `{"model":"test"}`, ResponseBody: `{"choices":[]}`, Outcome: "captured"}},
				CallerResponse:    &rememberapp.RememberDiagnosticExchange{Kind: "caller_response", ResponseBody: `{"isError":true}`, Outcome: "captured"},
			},
		},
	}
	logger := &rememberAttemptLogCapture{}
	server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{RememberAttempts: reader}, HealthConfig{}, logger)
	require.NoError(t, err)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	rec := do("/control/api/remember-attempts?team_id=" + teamID.String() + "&outcome=failed&limit=25&offset=2")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"attempt_id":"`+attemptID.String()+`"`)
	require.Equal(t, rememberapp.RememberAttemptDiagnosticFilter{TeamID: teamID.String(), Outcome: "failed", Limit: 25, Offset: 2}, reader.filter)

	rec = do("/control/api/teams/" + teamID.String() + "/remember-attempts/" + attemptID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"original_request"`)
	require.Contains(t, rec.Body.String(), `"provider_exchanges"`)
	require.Contains(t, rec.Body.String(), `"caller_response"`)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, teamID.String(), reader.teamID)
	require.Equal(t, attemptID.String(), reader.attemptID)

	var audit *rememberAttemptLogEntry
	for index := range logger.entries {
		if logger.entries[index].message == "control_remember_attempt_diagnostic_access" {
			audit = &logger.entries[index]
			break
		}
	}
	require.NotNil(t, audit)
	require.Equal(t, teamID.String(), logAttrValue(audit.attrs, "team_id"))
	require.Equal(t, attemptID.String(), logAttrValue(audit.attrs, "attempt_id"))
}

func TestControlPortalRememberAttemptValidationAndNotFound(t *testing.T) {
	for _, outcome := range []string{"completed", "rejected", "quarantined", "failed", "replayed"} {
		req := httptest.NewRequest(http.MethodGet, "/control/api/remember-attempts?outcome="+outcome, nil)
		ctx := echo.New().NewContext(req, httptest.NewRecorder())
		filter, err := controlRememberAttemptDiagnosticFilter(ctx)
		require.NoError(t, err)
		require.Equal(t, outcome, filter.Outcome)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/control/api/remember-attempts?outcome=unknown", nil)
	ctx := echo.New().NewContext(req, rec)
	_, err := controlRememberAttemptDiagnosticFilter(ctx)
	require.ErrorContains(t, err, "outcome")

	h := &controlPortalHandler{rememberAttempts: &controlRememberAttemptDiagnosticsStub{detailErr: rememberapp.ErrRememberAttemptDiagnosticNotFound}}
	ctx = withRememberAttemptParams(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec), uuid.NewString(), uuid.NewString())
	err = h.getRememberAttemptDiagnostic(ctx)
	require.ErrorContains(t, err, "remember attempt not found")
}

func TestControlPortalRememberAttemptErrorsAndBounds(t *testing.T) {
	e := echo.New()
	newContext := func(path string) echo.Context {
		return e.NewContext(httptest.NewRequest(http.MethodGet, path, nil), httptest.NewRecorder())
	}

	unavailable := &controlPortalHandler{}
	require.ErrorContains(t, unavailable.listRememberAttemptDiagnostics(newContext("/")), "unavailable")
	require.ErrorContains(t, unavailable.getRememberAttemptDiagnostic(newContext("/")), "unavailable")

	for _, path := range []string{"/?limit=101", "/?offset=-1", "/?team_id=bad", "/?outcome=unknown"} {
		_, err := controlRememberAttemptDiagnosticFilter(newContext(path))
		require.Error(t, err, path)
	}

	reader := &controlRememberAttemptDiagnosticsStub{
		listErr:   rememberapp.ErrRememberAttemptDiagnosticsUnavailable,
		detailErr: rememberapp.ErrRememberAttemptDiagnosticsUnavailable,
	}
	h := &controlPortalHandler{rememberAttempts: reader}
	require.ErrorContains(t, h.listRememberAttemptDiagnostics(newContext("/")), "unavailable")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), "bad", uuid.NewString())), "team ID")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), "bad")), "attempt ID")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), uuid.NewString())), "unavailable")

	reader.listErr = errors.New("list failed")
	require.ErrorContains(t, h.listRememberAttemptDiagnostics(newContext("/")), "list failed")
	reader.detailErr = rememberapp.ErrRememberAttemptDiagnosticNotFound
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), uuid.NewString())), "not found")
	reader.detailErr = errors.New("detail failed")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), uuid.NewString())), "detail failed")
}

func withRememberAttemptParams(ctx echo.Context, teamID, attemptID string) echo.Context {
	ctx.SetParamNames("teamId", "attemptId")
	ctx.SetParamValues(teamID, attemptID)
	return ctx
}

type controlRememberAttemptDiagnosticsStub struct {
	page      *rememberapp.RememberAttemptDiagnosticPage
	detail    *rememberapp.RememberAttemptDiagnosticDetail
	filter    rememberapp.RememberAttemptDiagnosticFilter
	teamID    string
	attemptID string
	listErr   error
	detailErr error
}

type rememberAttemptLogEntry struct {
	message string
	attrs   []httpcontract.LogAttr
}

type rememberAttemptLogCapture struct {
	entries []rememberAttemptLogEntry
}

func (l *rememberAttemptLogCapture) Info(message string, attrs ...httpcontract.LogAttr) {
	l.entries = append(l.entries, rememberAttemptLogEntry{message: message, attrs: append([]httpcontract.LogAttr(nil), attrs...)})
}
func (l *rememberAttemptLogCapture) Error(message string, _ error, attrs ...httpcontract.LogAttr) {
	l.entries = append(l.entries, rememberAttemptLogEntry{message: message, attrs: append([]httpcontract.LogAttr(nil), attrs...)})
}
func (l *rememberAttemptLogCapture) Warn(message string, attrs ...httpcontract.LogAttr) {
	l.entries = append(l.entries, rememberAttemptLogEntry{message: message, attrs: append([]httpcontract.LogAttr(nil), attrs...)})
}
func (*rememberAttemptLogCapture) Debug(string, ...httpcontract.LogAttr) {}
func (l *rememberAttemptLogCapture) With(...httpcontract.LogAttr) httpcontract.LogProvider {
	return l
}

func (s *controlRememberAttemptDiagnosticsStub) ListRememberAttemptDiagnostics(_ context.Context, filter rememberapp.RememberAttemptDiagnosticFilter) (*rememberapp.RememberAttemptDiagnosticPage, error) {
	s.filter = filter
	return s.page, s.listErr
}
func (s *controlRememberAttemptDiagnosticsStub) GetRememberAttemptDiagnostic(_ context.Context, teamID, attemptID string) (*rememberapp.RememberAttemptDiagnosticDetail, error) {
	s.teamID, s.attemptID = teamID, attemptID
	return s.detail, s.detailErr
}
