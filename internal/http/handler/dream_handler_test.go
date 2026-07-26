package handler

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

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

type dreamHandlerServiceStub struct {
	status     *dreamservice.StatusResult
	runs       []*dreamservice.RunCycleResult
	dreams     []*domain.Dream
	dream      *domain.Dream
	nextCursor string
	listOpts   dreamservice.ListOptions
	runsLimit  int
	profileIDs []string
	getErr     error
	listErr    error
}

func (s *dreamHandlerServiceStub) RunCycle(context.Context, string, dreamservice.RunCycleRequest) (*dreamservice.RunCycleResult, error) {
	return nil, nil
}

func (s *dreamHandlerServiceStub) List(_ context.Context, profileID string, opts dreamservice.ListOptions) ([]*domain.Dream, string, error) {
	s.profileIDs = append(s.profileIDs, profileID)
	s.listOpts = opts
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	return s.dreams, s.nextCursor, nil
}

func (s *dreamHandlerServiceStub) Get(_ context.Context, profileID, dreamID string) (*domain.Dream, error) {
	s.profileIDs = append(s.profileIDs, profileID)
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.dream != nil {
		s.dream.DreamID = dreamID
		return s.dream, nil
	}
	return &domain.Dream{DreamID: dreamID, ProfileID: profileID, Status: domain.DreamStatusProposed}, nil
}

func (s *dreamHandlerServiceStub) ListRuns(_ context.Context, profileID string, limit int) ([]*dreamservice.RunCycleResult, error) {
	s.profileIDs = append(s.profileIDs, profileID)
	s.runsLimit = limit
	return s.runs, nil
}

func (s *dreamHandlerServiceStub) Recall(context.Context, string, string, int) ([]*domain.Dream, error) {
	return nil, nil
}

func (s *dreamHandlerServiceStub) ResolveFeedback(context.Context, string, dreamservice.ResolveFeedbackRequest) (*dreamservice.ResolveFeedbackResult, error) {
	return &dreamservice.ResolveFeedbackResult{}, nil
}

func (s *dreamHandlerServiceStub) Status(_ context.Context, profileID string) (*dreamservice.StatusResult, error) {
	s.profileIDs = append(s.profileIDs, profileID)
	return s.status, nil
}

func (s *dreamHandlerServiceStub) EffectiveConfig(context.Context, string) (dreamservice.EffectiveConfig, error) {
	return dreamservice.EffectiveConfig{}, nil
}

func TestDreamHandlerRoutes(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	svc := &dreamHandlerServiceStub{
		status: &dreamservice.StatusResult{PendingCount: 2},
		runs: []*dreamservice.RunCycleResult{{
			RunID:       "run-1",
			ProfileID:   profileID.String(),
			RunDate:     "2026-06-14",
			StartedAt:   now,
			CompletedAt: now,
			Status:      "completed",
		}},
		dreams: []*domain.Dream{{
			DreamID:    "dream-1",
			ProfileID:  profileID.String(),
			Hypothesis: "A dream appears",
			Status:     domain.DreamStatusProposed,
			CreatedAt:  now,
			UpdatedAt:  now,
		}},
		nextCursor: "after-dream-1",
		dream: &domain.Dream{
			ProfileID:  profileID.String(),
			Hypothesis: "A dream appears",
			Status:     domain.DreamStatusProposed,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}
	h := NewDreamHandler(svc)
	e.Use(injectProfileMiddleware(profileID))
	e.GET("/ui/api/dreaming/status", h.Status)
	e.GET("/ui/api/dreaming/runs", h.Runs)
	e.GET("/ui/api/dreams", h.List)
	e.GET("/ui/api/dreams/:dreamId", h.Get)

	tests := []struct {
		path string
		want string
	}{
		{path: "/ui/api/dreaming/status", want: `"pending_count":2`},
		{path: "/ui/api/dreaming/runs?limit=3", want: `"run_id":"run-1"`},
		{path: "/ui/api/dreams?limit=4&status=proposed&cursor=next&sort=created_at&direction=asc", want: `"next_cursor":"after-dream-1"`},
		{path: "/ui/api/dreams/dream-1", want: `"dream_id":"dream-1"`},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), tt.want)
	}
	assert.Equal(t, 3, svc.runsLimit)
	assert.Equal(t, dreamservice.ListOptions{Limit: 4, Status: "proposed", Cursor: "next", Sort: "created_at", Direction: "asc"}, svc.listOpts)
	assert.Contains(t, svc.profileIDs, profileID.String())
}

func TestDreamHandlerRunsReturnsEmptyArrayForNoRuns(t *testing.T) {
	profileID := uuid.New()
	e := echo.New()
	h := NewDreamHandler(&dreamHandlerServiceStub{})
	e.Use(injectProfileMiddleware(profileID))
	e.GET("/ui/api/dreaming/runs", h.Runs)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/dreaming/runs", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"data":[]}`, rec.Body.String())
}

func TestDreamHandlerValidationAndNotFound(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	profileID := uuid.New()
	svc := &dreamHandlerServiceStub{getErr: dreamservice.ErrDreamNotFound}
	h := NewDreamHandler(svc)

	e.GET("/ui/api/dreaming/status", h.Status)
	req := httptest.NewRequest(http.MethodGet, "/ui/api/dreaming/status", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	e = echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(injectProfileMiddleware(profileID))
	e.GET("/ui/api/dreaming/runs", h.Runs)
	e.GET("/ui/api/dreams", h.List)
	e.GET("/ui/api/dreams/:dreamId", h.Get)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/dreaming/runs?limit=101", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "limit must be between 1 and 100")

	req = httptest.NewRequest(http.MethodGet, "/ui/api/dreams?status=unknown", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "status must be one of")

	req = httptest.NewRequest(http.MethodGet, "/ui/api/dreams?sort=bad", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "sort must be updated_at")

	req = httptest.NewRequest(http.MethodGet, "/ui/api/dreams?direction=sideways", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "direction must be asc or desc")

	svc.listErr = dreamservice.ErrInvalidDreamCursor
	req = httptest.NewRequest(http.MethodGet, "/ui/api/dreams?cursor=bad", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid cursor")

	req = httptest.NewRequest(http.MethodGet, "/ui/api/dreams/dream-missing", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "dream not found")

	limit, err := dreamLimit("")
	require.NoError(t, err)
	assert.Equal(t, defaultDreamListLimit, limit)
}
