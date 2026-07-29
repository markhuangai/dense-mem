package handler

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

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type unitAuditService struct {
	entries     []service.AuditLogEntry
	total       int
	err         error
	lastProfile string
	lastLimit   int
	lastOffset  int
	called      int
}

func (s *unitAuditService) List(_ context.Context, profileID string, limit, offset int) ([]service.AuditLogEntry, int, error) {
	s.called++
	s.lastProfile = profileID
	s.lastLimit = limit
	s.lastOffset = offset
	if s.err != nil {
		return nil, 0, s.err
	}
	return s.entries, s.total, nil
}

func TestAuditHandlerGetReturnsPaginatedAuditEntries(t *testing.T) {
	teamID := uuid.New()
	keyID := "key-1"
	profileID := teamID.String()
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	svc := &unitAuditService{
		total: 1,
		entries: []service.AuditLogEntry{{
			ID:            "audit-1",
			ProfileID:     &profileID,
			Timestamp:     now,
			Operation:     "CREATE",
			EntityType:    "profile",
			EntityID:      profileID,
			BeforePayload: map[string]interface{}{"old": "value"},
			AfterPayload:  map[string]interface{}{"new": "value"},
			ActorKeyID:    &keyID,
			ActorRole:     "admin",
			ClientIP:      "203.0.113.10",
			CorrelationID: "corr-1",
			Metadata:      map[string]interface{}{"source": "unit"},
		}},
	}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	handler := NewAuditHandler(svc)
	e.GET("/ui/api/team/audit-log", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/team/audit-log?limit=500&offset=2", nil)
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), &middleware.Principal{TeamID: teamID, Role: "admin"}))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, teamID.String(), svc.lastProfile)
	require.Equal(t, 100, svc.lastLimit)
	require.Equal(t, 2, svc.lastOffset)
	require.Contains(t, rec.Body.String(), `"id":"audit-1"`)
	require.Contains(t, rec.Body.String(), `"timestamp":"2026-05-30T12:00:00Z"`)
	require.Contains(t, rec.Body.String(), `"total":1`)
}

func TestAuditHandlerGetRejectsMissingPrincipalAndLegacyPath(t *testing.T) {
	svc := &unitAuditService{}
	handler := NewAuditHandler(svc)
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/ui/api/team/audit-log", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/team/audit-log", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), string(httperr.PROFILE_ID_REQUIRED))

	req = httptest.NewRequest(http.MethodGet, "/ui/api/team/profiles/not-a-uuid/audit-log", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, 0, svc.called)
}

func TestAuditHandlerGetPropagatesServiceErrorAndPureHelpers(t *testing.T) {
	teamID := uuid.New()
	svc := &unitAuditService{err: errors.New("list failed")}
	handler := NewAuditHandler(svc)
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/ui/api/team/audit-log", handler.Get)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/team/audit-log?limit=-1&offset=-3", nil)
	req = req.WithContext(middleware.SetPrincipalForTest(req.Context(), &middleware.Principal{TeamID: teamID, Role: "admin"}))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, 20, svc.lastLimit)
	require.Equal(t, 0, svc.lastOffset)

	entry := service.AuditLogEntry{
		ID:        "audit-2",
		Timestamp: time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
		Operation: "UPDATE",
	}
	resp := toAuditLogResponse(entry)
	require.Equal(t, "audit-2", resp.ID)
	require.Equal(t, "2026-05-30T12:00:00Z", resp.Timestamp)
}
