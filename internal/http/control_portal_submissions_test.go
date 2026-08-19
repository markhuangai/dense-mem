package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestControlPortalSubmissionRoutes(t *testing.T) {
	teamID, submissionID := uuid.New(), uuid.New()
	now := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	reader := &controlSubmissionDiagnosticsStub{
		page: &service.SubmissionDiagnosticPage{Items: []service.SubmissionDiagnosticSummary{{
			TeamID: teamID.String(), TeamName: "Staging", SubmissionID: submissionID.String(),
			ProcessingState: "failed", SubmittedAt: now,
			Error: &memoryservice.SubmissionStatusError{Code: "submission_processing_failed", Message: "submission processing failed", NextAction: "contact_operator", Remediation: "Contact an operator.", Retryable: false},
		}}, Total: 1},
		detail: &service.SubmissionDiagnosticDetail{
			TeamID: teamID.String(), TeamName: "Staging", OwnerProfileID: uuid.NewString(), EvidenceCount: 1,
			SubmissionStatusResult: memoryservice.SubmissionStatusResult{SubmissionID: submissionID.String(), SubmissionKind: "remember", ProcessingState: "failed", SearchState: "not_required", Evidence: []memoryservice.SubmissionEvidenceStatus{}, Errors: []memoryservice.SubmissionStatusError{}},
		},
	}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr: "127.0.0.1:8090", ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{Submissions: reader}, HealthConfig{}, nil)
	require.NoError(t, err)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	rec := do("/control/api/submissions?team_id=" + teamID.String() + "&processing_state=failed&limit=25&offset=2")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"submission_processing_failed"`)
	require.Equal(t, service.SubmissionDiagnosticFilter{TeamID: teamID.String(), ProcessingState: "failed", Limit: 25, Offset: 2}, reader.filter)

	rec = do("/control/api/teams/" + teamID.String() + "/submissions/" + submissionID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"submission_id":"`+submissionID.String()+`"`)
	require.Equal(t, teamID.String(), reader.teamID)
	require.Equal(t, submissionID.String(), reader.submissionID)
}

func TestControlPortalSubmissionValidationAndNotFound(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/control/api/submissions?processing_state=unknown", nil)
	c := e.NewContext(req, rec)
	_, err := controlSubmissionDiagnosticFilter(c)
	require.ErrorContains(t, err, "processing_state")

	h := &controlPortalHandler{submissions: &controlSubmissionDiagnosticsStub{err: service.ErrSubmissionDiagnosticNotFound}}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	c = e.NewContext(req, rec)
	c.SetParamNames("teamId", "submissionId")
	c.SetParamValues(uuid.NewString(), uuid.NewString())
	err = h.getSubmissionDiagnostic(c)
	require.ErrorContains(t, err, "submission not found")
}

type controlSubmissionDiagnosticsStub struct {
	page         *service.SubmissionDiagnosticPage
	detail       *service.SubmissionDiagnosticDetail
	filter       service.SubmissionDiagnosticFilter
	teamID       string
	submissionID string
	err          error
}

func (s *controlSubmissionDiagnosticsStub) ListSubmissionDiagnostics(_ context.Context, filter service.SubmissionDiagnosticFilter) (*service.SubmissionDiagnosticPage, error) {
	s.filter = filter
	return s.page, s.err
}

func (s *controlSubmissionDiagnosticsStub) GetSubmissionDiagnostic(_ context.Context, teamID, submissionID string) (*service.SubmissionDiagnosticDetail, error) {
	s.teamID, s.submissionID = teamID, submissionID
	return s.detail, s.err
}
