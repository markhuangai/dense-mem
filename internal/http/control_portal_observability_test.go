package http

import (
	"context"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
)

type controlOperationLogReaderStub struct {
	page    *domain.OperationLogPage
	filter  domain.OperationLogFilter
	listErr error
}

func (s *controlOperationLogReaderStub) ListOperationLogs(_ context.Context, filter domain.OperationLogFilter) (*domain.OperationLogPage, error) {
	s.filter = filter
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.page != nil {
		return s.page, nil
	}
	return &domain.OperationLogPage{}, nil
}

type controlRecallFeedbackReaderStub struct {
	page     *domain.RecallFeedbackEventPage
	event    *domain.RecallFeedbackEvent
	filter   domain.RecallFeedbackEventFilter
	recallID string
}

func (s *controlRecallFeedbackReaderStub) ListRecallFeedbackEvents(_ context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error) {
	s.filter = filter
	if s.page != nil {
		return s.page, nil
	}
	return &domain.RecallFeedbackEventPage{}, nil
}

func (s *controlRecallFeedbackReaderStub) GetRecallFeedbackEvent(_ context.Context, recallID string) (*domain.RecallFeedbackEvent, error) {
	s.recallID = recallID
	return s.event, nil
}

func TestControlPortalObservabilityRoutes(t *testing.T) {
	teamID := uuid.New()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	logs := &controlOperationLogReaderStub{page: &domain.OperationLogPage{
		Items: []domain.OperationLog{{
			ID:        uuid.New(),
			Timestamp: now,
			Severity:  "WARN",
			Message:   "Evidence assessment completed",
		}},
		Total: 1,
	}}
	feedback := &controlRecallFeedbackReaderStub{page: &domain.RecallFeedbackEventPage{
		Items: []domain.RecallFeedbackEvent{{
			RecallID:      "rec_1",
			CreatedAt:     now,
			Query:         "why was recall bad?",
			ResultCount:   1,
			SnapshotState: domain.RecallFeedbackSnapshotCaptured,
			Quality:       "low",
		}},
		Total: 1,
	}, event: &domain.RecallFeedbackEvent{
		RecallID:      "rec_1",
		CreatedAt:     now,
		Query:         "why was recall bad?",
		ResultRefs:    []domain.RecallFeedbackResultRef{{Type: domain.RecallFeedbackResultTypeEvidence, ID: "evidence-1", Rank: 1}},
		SnapshotState: domain.RecallFeedbackSnapshotCaptured,
	}}
	profiles := &controlProfileSvc{profiles: []*domain.Team{{ID: teamID, Name: "Default"}}}
	server, err := newControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, profiles, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Logs:           logs,
		RecallFeedback: feedback,
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	logFrom := "2026-06-01T00:00:00Z"
	logTo := "2026-06-23T00:00:00Z"
	rec := do("/control/api/logs?limit=5&offset=2&severity=warn&sort=severity&direction=asc&event=submission_failed&team_id=" + teamID.String() + "&reference_type=submission&reference_id=submission-1&from=" + logFrom + "&to=" + logTo + "&correlation_id=corr-1&invocation_id=inv-1&request_hash=hash-1&attempt_id=attempt-1&classification=replay&retryable=true")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Evidence assessment completed")
	expectedLogFrom, expectedLogTo := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, domain.OperationLogFilter{
		Limit:          5,
		Offset:         2,
		Severity:       "WARN",
		Sort:           "severity",
		Direction:      "asc",
		Event:          "submission_failed",
		CorrelationID:  "corr-1",
		InvocationID:   "inv-1",
		RequestHash:    "hash-1",
		AttemptID:      "attempt-1",
		Classification: "replay",
		Retryable:      boolPtr(true),
		TeamID:         &teamID,
		ReferenceType:  "submission",
		ReferenceID:    "submission-1",
		From:           &expectedLogFrom,
		To:             &expectedLogTo,
	}, logs.filter)

	rec = do("/control/api/recall-feedback-events?limit=5&offset=2&quality=low&include_pending=true&missing_context=true&irrelevant=false&from=2026-06-01T00:00:00Z&to=2026-06-23T00:00:00Z")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "why was recall bad?")
	assert.Equal(t, 5, feedback.filter.Limit)
	assert.Equal(t, 2, feedback.filter.Offset)
	assert.Equal(t, "low", feedback.filter.Quality)
	assert.True(t, feedback.filter.IncludePending)
	require.NotNil(t, feedback.filter.MissingContext)
	assert.True(t, *feedback.filter.MissingContext)
	require.NotNil(t, feedback.filter.Irrelevant)
	assert.False(t, *feedback.filter.Irrelevant)
	require.NotNil(t, feedback.filter.From)
	require.NotNil(t, feedback.filter.To)

	rec = do("/control/api/recall-feedback-events/rec_1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "evidence-1")
	assert.Equal(t, "rec_1", feedback.recallID)

}

func boolPtr(value bool) *bool { return &value }

func TestControlPortalObservabilityValidation(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodGet, "/control/api/logs?limit=501", nil)
	c := e.NewContext(req, rec)
	_, err := controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "limit must be between 1 and 500")

	req = httptest.NewRequest(http.MethodGet, "/control/api/telemetry?scope=unsupported", nil)
	c = e.NewContext(req, rec)
	_, err = controlTelemetryFilter(c)
	require.ErrorContains(t, err, "scope must be one of system, team, profile")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?severity=verbose", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "severity must be one of")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?sort=message", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "sort must be timestamp or severity")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?direction=sideways", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "direction must be asc or desc")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?classification=unknown", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "classification")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?retryable=maybe", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "retryable")
	for _, literal := range []string{"1", "0", "t", "f", "TRUE", "False"} {
		req = httptest.NewRequest(http.MethodGet, "/control/api/logs?retryable="+literal, nil)
		c = e.NewContext(req, rec)
		_, err = controlOperationLogsFilter(c)
		require.ErrorContains(t, err, "retryable must be true or false", literal)
	}

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?team_id=bad", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "team ID must be a valid UUID")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?from=bad", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "from must be RFC3339")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?from=2026-06-24T00:00:00Z&to=2026-06-23T00:00:00Z", nil)
	c = e.NewContext(req, rec)
	_, err = controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "from must be before or equal to to")

	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events?limit=501", nil)
	c = e.NewContext(req, rec)
	_, err = controlRecallFeedbackEventsFilter(c)
	require.ErrorContains(t, err, "limit must be between 1 and 500")

	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events?quality=bad", nil)
	c = e.NewContext(req, rec)
	_, err = controlRecallFeedbackEventsFilter(c)
	require.ErrorContains(t, err, "quality must be one of")

	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events?include_pending=maybe", nil)
	c = e.NewContext(req, rec)
	_, err = controlRecallFeedbackEventsFilter(c)
	require.ErrorContains(t, err, "include_pending must be true or false")

	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events?missing_context=maybe", nil)
	c = e.NewContext(req, rec)
	_, err = controlRecallFeedbackEventsFilter(c)
	require.ErrorContains(t, err, "missing_context must be true or false")

	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events?from=bad", nil)
	c = e.NewContext(req, rec)
	_, err = controlRecallFeedbackEventsFilter(c)
	require.ErrorContains(t, err, "from must be RFC3339")

	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events?from=2026-06-24T00:00:00Z&to=2026-06-23T00:00:00Z", nil)
	c = e.NewContext(req, rec)
	_, err = controlRecallFeedbackEventsFilter(c)
	require.ErrorContains(t, err, "from must be before or equal to to")

}

func TestControlPortalRequestLogURIOmitsCallbackCredentials(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/control/auth/callback?state=oauth-state&code=authorization-code", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	assert.Equal(t, "/control/auth/callback", requestLogURI(c))
}

func TestControlPortalObservabilityUnavailableAndNotFound(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	h := &controlPortalHandler{}

	require.ErrorContains(t, h.listOperationLogs(c), "operation logs unavailable")
	require.ErrorContains(t, h.listRecallFeedbackEvents(c), "recall feedback events unavailable")
	require.ErrorContains(t, h.getRecallFeedbackEvent(c), "recall feedback events unavailable")
	h.recallFeedback = &controlRecallFeedbackReaderStub{}
	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events/rec_missing", nil)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetParamNames("recallId")
	c.SetParamValues("rec_missing")
	err := h.getRecallFeedbackEvent(c)
	require.ErrorContains(t, err, "recall feedback event not found")
}

func TestControlDependencySnapshotErrorAndDegraded(t *testing.T) {
	snapshot := controlDependencySnapshot(context.Background(), HealthConfig{
		Checks: []HealthCheck{
			{Name: "postgres", Check: func(context.Context) error { return assert.AnError }},
			{Name: "missing"},
		},
		Degraded: true,
	})

	require.Len(t, snapshot, 2)
	assert.Equal(t, "postgres", snapshot[0].Name)
	assert.Equal(t, "error", snapshot[0].Status)
	require.NotNil(t, snapshot[0].Message)
	assert.Equal(t, "Dependency check failed.", *snapshot[0].Message)
	require.NotNil(t, snapshot[0].ReasonCode)
	assert.Equal(t, "check_failed", *snapshot[0].ReasonCode)
	assert.Equal(t, "redis", snapshot[1].Name)
	assert.Equal(t, "degraded", snapshot[1].Status)
	require.NotNil(t, snapshot[1].ReasonCode)
	assert.Equal(t, "single_node_mode", *snapshot[1].ReasonCode)
}

var _ operations.OperationLogReader = (*controlOperationLogReaderStub)(nil)
