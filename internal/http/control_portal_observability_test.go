package http

import (
	"context"
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
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
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

type controlDreamServiceStub struct {
	status     *dreamservice.StatusResult
	runs       []*dreamservice.RunCycleResult
	dreams     []*domain.Dream
	dream      *domain.Dream
	nextCursor string
	listOpts   dreamservice.ListOptions
	runsLimit  int
	profileID  string
	getErr     error
	listErr    error
}

func (s *controlDreamServiceStub) RunCycle(context.Context, string, dreamservice.RunCycleRequest) (*dreamservice.RunCycleResult, error) {
	return nil, nil
}

func (s *controlDreamServiceStub) List(_ context.Context, profileID string, opts dreamservice.ListOptions) ([]*domain.Dream, string, error) {
	s.profileID = profileID
	s.listOpts = opts
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	return s.dreams, s.nextCursor, nil
}

func (s *controlDreamServiceStub) Get(_ context.Context, profileID, dreamID string) (*domain.Dream, error) {
	s.profileID = profileID
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.dream != nil {
		s.dream.DreamID = dreamID
		return s.dream, nil
	}
	return &domain.Dream{DreamID: dreamID, ProfileID: profileID, Status: domain.DreamStatusProposed}, nil
}

func (s *controlDreamServiceStub) ListRuns(_ context.Context, profileID string, limit int) ([]*dreamservice.RunCycleResult, error) {
	s.profileID = profileID
	s.runsLimit = limit
	return s.runs, nil
}

func (s *controlDreamServiceStub) Recall(context.Context, string, string, int) ([]*domain.Dream, error) {
	return nil, nil
}

func (s *controlDreamServiceStub) ResolveFeedback(context.Context, string, dreamservice.ResolveFeedbackRequest) (*dreamservice.ResolveFeedbackResult, error) {
	return &dreamservice.ResolveFeedbackResult{Fragment: &fragmentservice.CreateResult{}}, nil
}

func (s *controlDreamServiceStub) Status(_ context.Context, profileID string) (*dreamservice.StatusResult, error) {
	s.profileID = profileID
	return s.status, nil
}

func (s *controlDreamServiceStub) EffectiveConfig(context.Context, string) (dreamservice.EffectiveConfig, error) {
	return dreamservice.EffectiveConfig{}, nil
}

func TestControlPortalObservabilityRoutes(t *testing.T) {
	teamID := uuid.New()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	logs := &controlOperationLogReaderStub{page: &domain.OperationLogPage{
		Items: []domain.OperationLog{{
			ID:        uuid.New(),
			Timestamp: now,
			Severity:  "WARN",
			Message:   "dream cycle completed",
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
		ResultRefs:    []domain.RecallFeedbackResultRef{{Type: domain.RecallFeedbackResultTypeFragment, ID: "fragment-1", Rank: 1}},
		SnapshotState: domain.RecallFeedbackSnapshotCaptured,
	}}
	dreams := &controlDreamServiceStub{
		status: &dreamservice.StatusResult{PendingCount: 1},
		runs: []*dreamservice.RunCycleResult{{
			RunID:     "run-1",
			ProfileID: teamID.String(),
			RunDate:   "2026-06-14",
			Status:    "completed",
		}},
		dreams: []*domain.Dream{{
			DreamID:    "dream-1",
			ProfileID:  teamID.String(),
			Hypothesis: "A control dream appears",
			Status:     domain.DreamStatusProposed,
			CreatedAt:  now,
			UpdatedAt:  now,
		}},
		nextCursor: "after-control-dream-1",
		dream: &domain.Dream{
			ProfileID:  teamID.String(),
			Hypothesis: "A control dream appears",
			Status:     domain.DreamStatusProposed,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}
	profiles := &controlProfileSvc{profiles: []*domain.Profile{{ID: teamID, Name: "Default"}}}
	server, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, profiles, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Logs:           logs,
		RecallFeedback: feedback,
		Dreams:         dreams,
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}

	rec := do("/control/api/logs?limit=5&offset=2&severity=warn&sort=severity&direction=asc")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "dream cycle completed")
	assert.Equal(t, domain.OperationLogFilter{
		Limit:     5,
		Offset:    2,
		Severity:  "WARN",
		Sort:      "severity",
		Direction: "asc",
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
	assert.Contains(t, rec.Body.String(), "fragment-1")
	assert.Equal(t, "rec_1", feedback.recallID)

	rec = do("/control/api/teams/" + teamID.String() + "/dreaming/status")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"pending_count":1`)

	rec = do("/control/api/teams/" + teamID.String() + "/dreaming/runs?limit=3")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"run_id":"run-1"`)
	assert.Equal(t, 3, dreams.runsLimit)

	rec = do("/control/api/teams/" + teamID.String() + "/dreams?limit=4&status=proposed&cursor=next&sort=last_evaluated_at&direction=asc")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "A control dream appears")
	assert.Contains(t, rec.Body.String(), `"next_cursor":"after-control-dream-1"`)
	assert.Equal(t, teamID.String(), dreams.profileID)
	assert.Equal(t, dreamservice.ListOptions{Limit: 4, Status: "proposed", Cursor: "next", Sort: "last_evaluated_at", Direction: "asc"}, dreams.listOpts)

	rec = do("/control/api/teams/" + teamID.String() + "/dreams/dream-1")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"dream_id":"dream-1"`)
}

func TestControlPortalObservabilityValidation(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/control/api/logs?limit=501", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_, err := controlOperationLogsFilter(c)
	require.ErrorContains(t, err, "limit must be between 1 and 500")

	req = httptest.NewRequest(http.MethodGet, "/control/api/logs?severity=trace", nil)
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

	limit, err := controlDreamLimit("")
	require.NoError(t, err)
	assert.Equal(t, 20, limit)
	_, err = controlDreamLimit("101")
	require.ErrorContains(t, err, "limit must be between 1 and 100")

	req = httptest.NewRequest(http.MethodGet, "/control/api/dreams?status=unknown", nil)
	c = e.NewContext(req, rec)
	_, err = controlDreamListOptions(c)
	require.ErrorContains(t, err, "status must be one of")

	req = httptest.NewRequest(http.MethodGet, "/control/api/dreams?sort=bad", nil)
	c = e.NewContext(req, rec)
	_, err = controlDreamListOptions(c)
	require.ErrorContains(t, err, "sort must be updated_at")

	req = httptest.NewRequest(http.MethodGet, "/control/api/dreams?direction=sideways", nil)
	c = e.NewContext(req, rec)
	_, err = controlDreamListOptions(c)
	require.ErrorContains(t, err, "direction must be asc or desc")
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
	require.ErrorContains(t, h.getTeamDreamingStatus(c), "dream service unavailable")
	require.ErrorContains(t, h.listTeamDreamingRuns(c), "dream service unavailable")
	require.ErrorContains(t, h.listTeamDreams(c), "dream service unavailable")
	require.ErrorContains(t, h.getTeamDream(c), "dream service unavailable")

	teamID := uuid.New()
	h.dreams = &controlDreamServiceStub{getErr: dreamservice.ErrDreamNotFound}
	req = httptest.NewRequest(http.MethodGet, "/control/api/teams/"+teamID.String()+"/dreams/dream-missing", nil)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetParamNames("teamId", "dreamId")
	c.SetParamValues(teamID.String(), "dream-missing")
	err := h.getTeamDream(c)
	require.ErrorContains(t, err, "dream not found")

	h.dreams = &controlDreamServiceStub{listErr: dreamservice.ErrInvalidDreamCursor}
	req = httptest.NewRequest(http.MethodGet, "/control/api/teams/"+teamID.String()+"/dreams?cursor=bad", nil)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetParamNames("teamId")
	c.SetParamValues(teamID.String())
	err = h.listTeamDreams(c)
	require.ErrorContains(t, err, "invalid cursor")

	h.recallFeedback = &controlRecallFeedbackReaderStub{}
	req = httptest.NewRequest(http.MethodGet, "/control/api/recall-feedback-events/rec_missing", nil)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetParamNames("recallId")
	c.SetParamValues("rec_missing")
	err = h.getRecallFeedbackEvent(c)
	require.ErrorContains(t, err, "recall feedback event not found")
}

func TestControlDependencySnapshotErrorAndDegraded(t *testing.T) {
	snapshot := controlDependencySnapshot(context.Background(), HealthConfig{
		Checks: []HealthCheck{
			{Name: "postgres", Check: func(context.Context) error { return assert.AnError }},
			{Name: "migration", Optional: true, Check: func(context.Context) error { return assert.AnError }},
			{Name: "missing"},
		},
		Degraded: true,
	})

	require.Len(t, snapshot, 3)
	assert.Equal(t, "postgres", snapshot[0].Name)
	assert.Equal(t, "error", snapshot[0].Status)
	require.NotNil(t, snapshot[0].Message)
	assert.Contains(t, *snapshot[0].Message, assert.AnError.Error())
	assert.Equal(t, "migration", snapshot[1].Name)
	assert.Equal(t, "degraded", snapshot[1].Status)
	require.NotNil(t, snapshot[1].Message)
	assert.Contains(t, *snapshot[1].Message, assert.AnError.Error())
	assert.Equal(t, "redis", snapshot[2].Name)
	assert.Equal(t, "degraded", snapshot[2].Status)
}

var _ service.OperationLogReader = (*controlOperationLogReaderStub)(nil)
var _ dreamservice.Service = (*controlDreamServiceStub)(nil)
