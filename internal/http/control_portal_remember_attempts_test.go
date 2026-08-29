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
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestControlPortalRememberAttemptRoutes(t *testing.T) {
	teamID, attemptID, artifactID := uuid.New(), uuid.New(), uuid.New()
	reader := &controlRememberAttemptDiagnosticsStub{
		page:     &service.RememberAttemptDiagnosticPage{Items: []service.RememberAttemptDiagnosticSummary{{TeamID: teamID.String(), AttemptID: attemptID.String(), Outcome: "failed", CreatedAt: time.Now().UTC()}}, Total: 1},
		detail:   &service.RememberAttemptDiagnosticDetail{RememberAttemptDiagnosticSummary: service.RememberAttemptDiagnosticSummary{TeamID: teamID.String(), AttemptID: attemptID.String(), Outcome: "failed"}, Events: []service.RememberAttemptDiagnosticEvent{}, Artifacts: []service.RememberFailureArtifactDescriptor{{ArtifactID: artifactID.String(), ArtifactKind: "failure", ContentType: "application/json", ByteCount: 20, ContentSHA256: "sha256:test"}}},
		artifact: &service.RememberFailureArtifact{ArtifactID: artifactID.String(), ContentType: "application/json", Content: []byte(`{"phase":"assessment"}`)},
	}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{RememberAttempts: reader}, HealthConfig{}, nil)
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
	require.Equal(t, service.RememberAttemptDiagnosticFilter{TeamID: teamID.String(), Outcome: "failed", Limit: 25, Offset: 2}, reader.filter)

	rec = do("/control/api/teams/" + teamID.String() + "/remember-attempts/" + attemptID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"attempt_id":"`+attemptID.String()+`"`)
	require.Equal(t, teamID.String(), reader.teamID)
	require.Equal(t, attemptID.String(), reader.attemptID)

	rec = do("/control/api/teams/" + teamID.String() + "/remember-attempts/" + attemptID.String() + "/artifacts/" + artifactID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, `{"phase":"assessment"}`, rec.Body.String())
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
}

func TestControlPortalRememberAttemptValidationAndNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	e := httptest.NewRequest(http.MethodGet, "/control/api/remember-attempts?outcome=unknown", nil)
	ctx := echo.New().NewContext(e, rec)
	_, err := controlRememberAttemptDiagnosticFilter(ctx)
	require.ErrorContains(t, err, "outcome")

	h := &controlPortalHandler{rememberAttempts: &controlRememberAttemptDiagnosticsStub{detailErr: service.ErrRememberAttemptDiagnosticNotFound}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx = echo.New().NewContext(req, rec)
	ctx.SetParamNames("teamId", "attemptId")
	ctx.SetParamValues(uuid.NewString(), uuid.NewString())
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
	require.ErrorContains(t, unavailable.getRememberFailureArtifact(newContext("/")), "unavailable")

	for _, path := range []string{"/?limit=101", "/?offset=-1", "/?team_id=bad", "/?outcome=unknown"} {
		_, err := controlRememberAttemptDiagnosticFilter(newContext(path))
		require.Error(t, err, path)
	}

	reader := &controlRememberAttemptDiagnosticsStub{
		listErr:     service.ErrRememberAttemptDiagnosticsUnavailable,
		detailErr:   service.ErrRememberAttemptDiagnosticsUnavailable,
		artifactErr: service.ErrRememberAttemptDiagnosticsUnavailable,
	}
	h := &controlPortalHandler{rememberAttempts: reader}
	require.ErrorContains(t, h.listRememberAttemptDiagnostics(newContext("/")), "unavailable")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), "bad", uuid.NewString())), "team ID")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), "bad")), "attempt ID")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), uuid.NewString())), "unavailable")
	require.ErrorContains(t, h.getRememberFailureArtifact(withArtifactParams(newContext("/"), "bad", uuid.NewString(), uuid.NewString())), "team ID")
	require.ErrorContains(t, h.getRememberFailureArtifact(withArtifactParams(newContext("/"), uuid.NewString(), "bad", uuid.NewString())), "attempt ID")
	require.ErrorContains(t, h.getRememberFailureArtifact(withArtifactParams(newContext("/"), uuid.NewString(), uuid.NewString(), "bad")), "artifact ID")
	require.ErrorContains(t, h.getRememberFailureArtifact(withArtifactParams(newContext("/"), uuid.NewString(), uuid.NewString(), uuid.NewString())), "unavailable")

	reader.listErr = errors.New("list failed")
	require.ErrorContains(t, h.listRememberAttemptDiagnostics(newContext("/")), "list failed")
	reader.detailErr = service.ErrRememberAttemptDiagnosticNotFound
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), uuid.NewString())), "not found")
	reader.detailErr = errors.New("detail failed")
	require.ErrorContains(t, h.getRememberAttemptDiagnostic(withRememberAttemptParams(newContext("/"), uuid.NewString(), uuid.NewString())), "detail failed")
	reader.artifactErr = service.ErrRememberFailureArtifactNotFound
	require.ErrorContains(t, h.getRememberFailureArtifact(withArtifactParams(newContext("/"), uuid.NewString(), uuid.NewString(), uuid.NewString())), "not found")
	reader.artifactErr = errors.New("artifact failed")
	require.ErrorContains(t, h.getRememberFailureArtifact(withArtifactParams(newContext("/"), uuid.NewString(), uuid.NewString(), uuid.NewString())), "artifact failed")
}

func withRememberAttemptParams(ctx echo.Context, teamID, attemptID string) echo.Context {
	ctx.SetParamNames("teamId", "attemptId")
	ctx.SetParamValues(teamID, attemptID)
	return ctx
}

func withArtifactParams(ctx echo.Context, teamID, attemptID, artifactID string) echo.Context {
	ctx.SetParamNames("teamId", "attemptId", "artifactId")
	ctx.SetParamValues(teamID, attemptID, artifactID)
	return ctx
}

type controlRememberAttemptDiagnosticsStub struct {
	page        *service.RememberAttemptDiagnosticPage
	detail      *service.RememberAttemptDiagnosticDetail
	artifact    *service.RememberFailureArtifact
	filter      service.RememberAttemptDiagnosticFilter
	teamID      string
	attemptID   string
	artifactID  string
	listErr     error
	detailErr   error
	artifactErr error
}

func (s *controlRememberAttemptDiagnosticsStub) ListRememberAttemptDiagnostics(_ context.Context, filter service.RememberAttemptDiagnosticFilter) (*service.RememberAttemptDiagnosticPage, error) {
	s.filter = filter
	return s.page, s.listErr
}
func (s *controlRememberAttemptDiagnosticsStub) GetRememberAttemptDiagnostic(_ context.Context, teamID, attemptID string) (*service.RememberAttemptDiagnosticDetail, error) {
	s.teamID, s.attemptID = teamID, attemptID
	return s.detail, s.detailErr
}
func (s *controlRememberAttemptDiagnosticsStub) GetRememberFailureArtifact(_ context.Context, teamID, attemptID, artifactID string) (*service.RememberFailureArtifact, error) {
	s.teamID, s.attemptID, s.artifactID = teamID, attemptID, artifactID
	return s.artifact, s.artifactErr
}
