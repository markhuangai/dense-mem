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
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

type controlSubmissionDiagnosticsReader struct {
	page        *service.SubmissionDiagnosticPage
	listErr     error
	detail      *service.SubmissionDiagnosticDetail
	detailErr   error
	artifact    *repository.RememberFailureArtifact
	artifactErr error
}

func (r *controlSubmissionDiagnosticsReader) ListSubmissionDiagnostics(context.Context, service.SubmissionDiagnosticFilter) (*service.SubmissionDiagnosticPage, error) {
	return r.page, r.listErr
}

func (r *controlSubmissionDiagnosticsReader) GetSubmissionDiagnostic(context.Context, string, string) (*service.SubmissionDiagnosticDetail, error) {
	return r.detail, r.detailErr
}

func (r *controlSubmissionDiagnosticsReader) GetRememberFailureArtifact(context.Context, string, string, string) (*repository.RememberFailureArtifact, error) {
	return r.artifact, r.artifactErr
}

func TestControlPortalRememberAttemptsRoutesAndArtifacts(t *testing.T) {
	teamID, attemptID, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	repo := &controlSubmissionDiagnosticsReader{
		page: &service.SubmissionDiagnosticPage{Total: 1, Items: []service.SubmissionDiagnosticSummary{{SubmissionID: attemptID, ProcessingState: "completed"}}},
		detail: &service.SubmissionDiagnosticDetail{SubmissionStatusResult: memoryservice.SubmissionStatusResult{
			ContractVersion: "dense-mem.v2.6.1", SubmissionID: attemptID, SubmissionKind: "remember", ProcessingState: "completed", SearchState: "current",
			Evidence: []memoryservice.SubmissionEvidenceStatus{}, RelationshipResults: []memoryservice.SubmissionRelationshipResult{}, Errors: []memoryservice.SubmissionStatusError{},
		}},
		artifact: &repository.RememberFailureArtifact{ContentType: "application/json", Content: []byte(`{"ok":true}`), ExpiresAt: time.Now().UTC().Add(time.Hour)},
	}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{Submissions: repo}, HealthConfig{}, nil)
	require.NoError(t, err)

	for _, test := range []struct {
		path string
		want int
		body string
	}{
		{path: "/control/api/remember-attempts?limit=10&offset=2&team_id=" + teamID + "&processing_state=completed", want: http.StatusOK, body: attemptID},
		{path: "/control/api/teams/" + teamID + "/remember-attempts/" + attemptID, want: http.StatusOK, body: "dense-mem.v2.6.1"},
		{path: "/control/api/teams/" + teamID + "/remember-attempts/" + attemptID + "/artifacts/" + artifactID, want: http.StatusOK, body: `{"ok":true}`},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		require.Equal(t, test.want, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), test.body)
		if len(test.path) > 10 && test.path[len(test.path)-len(artifactID)-1:] == "/"+artifactID {
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		}
	}
}

func TestControlPortalRememberAttemptsBoundsInvalidAndUnavailableResults(t *testing.T) {
	teamID, attemptID, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, test := range []struct {
		name        string
		path        string
		listErr     error
		detailErr   error
		artifactErr error
		want        int
	}{
		{name: "invalid limit", path: "/control/api/remember-attempts?limit=0", want: http.StatusUnprocessableEntity},
		{name: "invalid team", path: "/control/api/remember-attempts?team_id=bad", want: http.StatusUnprocessableEntity},
		{name: "invalid state", path: "/control/api/remember-attempts?processing_state=processing", want: http.StatusUnprocessableEntity},
		{name: "list unavailable", path: "/control/api/remember-attempts", listErr: service.ErrSubmissionDiagnosticsUnavailable, want: http.StatusServiceUnavailable},
		{name: "detail missing", path: "/control/api/teams/" + teamID + "/remember-attempts/" + attemptID, detailErr: service.ErrSubmissionDiagnosticNotFound, want: http.StatusNotFound},
		{name: "artifact unavailable", path: "/control/api/teams/" + teamID + "/remember-attempts/" + attemptID + "/artifacts/" + artifactID, artifactErr: service.ErrSubmissionDiagnosticsUnavailable, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &controlSubmissionDiagnosticsReader{listErr: test.listErr, detailErr: test.detailErr, artifactErr: test.artifactErr}
			server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{Submissions: repo}, HealthConfig{}, nil)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			require.Equal(t, test.want, rec.Code, rec.Body.String())
		})
	}
	_, err := controlSubmissionDiagnosticFilter(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?limit=nope", nil), httptest.NewRecorder()))
	require.Error(t, err)
	_, err = controlSubmissionDiagnosticFilter(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?offset=-1", nil), httptest.NewRecorder()))
	require.NoError(t, err)

	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{}, HealthConfig{}, nil)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/control/api/remember-attempts", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)

	// Generic repository errors remain bounded by the standard HTTP error path.
	reader := &controlSubmissionDiagnosticsReader{detailErr: errors.New("database details")}
	server, err = NewControlPortalServerWithMetricsAndTelemetry(&config.Config{ControlPortalToken: "secret"}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{Submissions: reader}, HealthConfig{}, nil)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodGet, "/control/api/teams/"+teamID+"/remember-attempts/"+attemptID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "database details")
}
