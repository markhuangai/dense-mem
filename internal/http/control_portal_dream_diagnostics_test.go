package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/dream"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

type dreamDiagnosticsServiceStub struct {
	page          *dream.DreamDiagnosticPage
	teamID        string
	runID         string
	limit         int
	cursor        string
	item          *dream.DreamDiagnostic
	listErr       error
	hypothesisErr error
}

func (s *dreamDiagnosticsServiceStub) List(_ context.Context, teamID, runID string, limit int, cursor string) (*dream.DreamDiagnosticPage, error) {
	s.teamID, s.runID, s.limit, s.cursor = teamID, runID, limit, cursor
	return s.page, s.listErr
}

func (s *dreamDiagnosticsServiceStub) ListForHypothesis(_ context.Context, teamID, hypothesisID string, limit int, cursor string) (*dream.DreamDiagnosticPage, error) {
	s.teamID, s.runID, s.limit, s.cursor = teamID, hypothesisID, limit, cursor
	return s.page, s.hypothesisErr
}

func (s *dreamDiagnosticsServiceStub) Get(_ context.Context, teamID, runID, captureID string) (*dream.DreamDiagnostic, error) {
	s.teamID, s.runID = teamID, runID
	if s.item != nil {
		s.item.CaptureID = captureID
	}
	return s.item, nil
}

var _ dream.DiagnosticService = (*dreamDiagnosticsServiceStub)(nil)

func TestControlPortalDreamDiagnosticsUsesScopedRunAndCursor(t *testing.T) {
	e := echo.New()
	teamID, runID := uuid.New(), uuid.New()
	service := &dreamDiagnosticsServiceStub{page: &dream.DreamDiagnosticPage{NextCursor: "next"}}
	h := &controlPortalHandler{dreamDiagnostics: service}
	req := httptest.NewRequest(http.MethodGet, "/control/api/teams/"+teamID.String()+"/dreaming/runs/"+runID.String()+"/diagnostics?limit=50&cursor=abc", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("teamId", "runId")
	c.SetParamValues(teamID.String(), runID.String())

	require.NoError(t, h.listTeamDreamDiagnostics(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, teamID.String(), service.teamID)
	require.Equal(t, runID.String(), service.runID)
	require.Equal(t, 50, service.limit)
	require.Equal(t, "abc", service.cursor)
}

func TestControlPortalDreamDiagnosticDetailPreventsCachingAndRecordsAccess(t *testing.T) {
	e := echo.New()
	teamID, runID, diagnosticID := uuid.New(), uuid.New(), uuid.New()
	service := &dreamDiagnosticsServiceStub{item: &dream.DreamDiagnostic{}}
	logger := &rememberAttemptLogCapture{}
	h := &controlPortalHandler{dreamDiagnostics: service, logger: logger}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("teamId", "runId", "diagnosticId")
	c.SetParamValues(teamID.String(), runID.String(), diagnosticID.String())

	require.NoError(t, h.getTeamDreamDiagnostic(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, teamID.String(), service.teamID)
	require.Equal(t, runID.String(), service.runID)
	require.Equal(t, diagnosticID.String(), service.item.CaptureID)

	var access *rememberAttemptLogEntry
	for index := range logger.entries {
		if logger.entries[index].message == "control_dream_diagnostic_access" {
			access = &logger.entries[index]
			break
		}
	}
	require.NotNil(t, access)
	require.Equal(t, teamID.String(), logAttrValue(access.attrs, "team_id"))
	require.Equal(t, runID.String(), logAttrValue(access.attrs, "run_id"))
	require.Equal(t, diagnosticID.String(), logAttrValue(access.attrs, "diagnostic_id"))
}

func TestControlPortalDreamDiagnosticsRejectsInvalidLimit(t *testing.T) {
	e := echo.New()
	teamID, runID := uuid.New(), uuid.New()
	h := &controlPortalHandler{dreamDiagnostics: &dreamDiagnosticsServiceStub{page: &dream.DreamDiagnosticPage{}}}
	req := httptest.NewRequest(http.MethodGet, "/?limit=101", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("teamId", "runId")
	c.SetParamValues(teamID.String(), runID.String())
	require.ErrorContains(t, h.listTeamDreamDiagnostics(c), "limit must be between 1 and 100")
}

func TestControlPortalDreamDiagnosticsMapsInvalidCursor(t *testing.T) {
	e := echo.New()
	teamID, runID := uuid.New(), uuid.New()
	service := &dreamDiagnosticsServiceStub{page: &dream.DreamDiagnosticPage{}, listErr: dreamcontract.ErrInvalidDreamDiagnosticCursor, hypothesisErr: dreamcontract.ErrInvalidDreamDiagnosticCursor}
	h := &controlPortalHandler{dreamDiagnostics: service}
	for _, route := range []struct {
		name  string
		setup func(echo.Context)
	}{
		{name: "run", setup: func(c echo.Context) {
			c.SetParamNames("teamId", "runId")
			c.SetParamValues(teamID.String(), runID.String())
		}},
		{name: "hypothesis", setup: func(c echo.Context) {
			c.SetParamNames("teamId", "dreamId")
			c.SetParamValues(teamID.String(), runID.String())
		}},
	} {
		req := httptest.NewRequest(http.MethodGet, "/?cursor=malformed", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		route.setup(c)
		var err error
		if route.name == "run" {
			err = h.listTeamDreamDiagnostics(c)
		} else {
			err = h.listTeamDreamDiagnosticsForHypothesis(c)
		}
		require.Error(t, err)
		apiErr, ok := err.(*httperr.APIError)
		require.True(t, ok)
		require.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
	}
}
