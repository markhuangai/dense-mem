package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

func TestUsageMetricsMiddleware_RecordsAuthenticatedRouteTemplate(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	recorder := &captureUsageMetricsRecorder{}

	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal := &Principal{TeamID: teamID, KeyID: keyID}
			ctx := context.WithValue(c.Request().Context(), principalContextKey{}, principal)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(UsageMetricsMiddleware(recorder))
	e.GET("/api/v1/fragments/:id", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fragments/abc", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, recorder.events, 1)
	require.Equal(t, teamID, recorder.events[0].TeamID)
	require.Equal(t, keyID, recorder.events[0].KeyID)
	require.Equal(t, "/api/v1/fragments/:id", recorder.events[0].Route)
	require.Equal(t, http.StatusNoContent, recorder.events[0].Status)
}

func TestUsageMetricsMiddleware_RecordsTypedErrors(t *testing.T) {
	recorder := &captureUsageMetricsRecorder{}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal := &Principal{TeamID: uuid.New(), KeyID: uuid.New()}
			ctx := context.WithValue(c.Request().Context(), principalContextKey{}, principal)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(UsageMetricsMiddleware(recorder))
	e.GET("/api/v1/recall", func(c echo.Context) error {
		return httperr.New(httperr.VALIDATION_ERROR, "bad query")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Len(t, recorder.events, 1)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.events[0].Status)
}

type captureUsageMetricsRecorder struct {
	events []domain.UsageMetricEvent
}

func (r *captureUsageMetricsRecorder) RecordRequest(_ context.Context, event domain.UsageMetricEvent) {
	r.events = append(r.events, event)
}
