package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestUsageMetricsMiddlewareSkipsMissingRecorderOrPrincipal(t *testing.T) {
	e := echo.New()
	e.Use(UsageMetricsMiddleware(nil))
	e.GET("/nil-recorder", func(c echo.Context) error {
		return c.NoContent(http.StatusAccepted)
	})

	req := httptest.NewRequest(http.MethodGet, "/nil-recorder", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)

	recorder := &captureUsageMetricsRecorder{}
	e = echo.New()
	e.Use(UsageMetricsMiddleware(recorder))
	e.GET("/missing-principal", func(c echo.Context) error {
		return c.NoContent(http.StatusAccepted)
	})

	req = httptest.NewRequest(http.MethodGet, "/missing-principal", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Empty(t, recorder.events)
}

func TestTelemetryHTTPMiddlewareRecordsRouteTemplate(t *testing.T) {
	recorder := &captureHTTPMetricsRecorder{}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(TelemetryHTTPMiddleware(recorder))
	e.GET("/api/v1/fragments/:id", func(c echo.Context) error {
		return httperr.New(httperr.VALIDATION_ERROR, "bad id")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/fragments/bad", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Len(t, recorder.events, 1)
	require.Equal(t, "/api/v1/fragments/:id", recorder.events[0].Route)
	require.Equal(t, http.MethodGet, recorder.events[0].Method)
	require.Equal(t, http.StatusUnprocessableEntity, recorder.events[0].Status)
	require.GreaterOrEqual(t, recorder.events[0].Duration, time.Duration(0))
}

func TestUsageStatusCoversFallbackBranches(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.Equal(t, http.StatusOK, usageStatus(c, nil))

	c.Response().Status = http.StatusCreated
	require.Equal(t, http.StatusCreated, usageStatus(c, nil))

	require.Equal(t, http.StatusNotFound, usageStatus(c, echo.NewHTTPError(http.StatusNotFound, "missing")))

	c.Response().Status = http.StatusTooManyRequests
	require.Equal(t, http.StatusTooManyRequests, usageStatus(c, context.Canceled))

	c.Response().Status = 0
	require.Equal(t, http.StatusInternalServerError, usageStatus(c, context.Canceled))
}

func TestUsageMetricsRecordsUnknownRouteWhenTemplateMissing(t *testing.T) {
	recorder := &captureUsageMetricsRecorder{}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/raw-path", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	principal := &Principal{TeamID: uuid.New(), KeyID: uuid.New()}
	ctx := context.WithValue(req.Context(), principalContextKey{}, principal)
	c.SetRequest(req.WithContext(ctx))

	recordUsageMetric(c, recorder, time.Now(), nil)

	require.Len(t, recorder.events, 1)
	require.Equal(t, "unknown", recorder.events[0].Route)
}

type captureUsageMetricsRecorder struct {
	events []domain.UsageMetricEvent
}

func (r *captureUsageMetricsRecorder) RecordRequest(_ context.Context, event domain.UsageMetricEvent) {
	r.events = append(r.events, event)
}

type captureHTTPMetricsRecorder struct {
	events []captureHTTPMetricEvent
}

type captureHTTPMetricEvent struct {
	Route    string
	Method   string
	Status   int
	Duration time.Duration
}

func (r *captureHTTPMetricsRecorder) ObserveHTTPRequest(_ context.Context, route, method string, status int, duration time.Duration) {
	r.events = append(r.events, captureHTTPMetricEvent{Route: route, Method: method, Status: status, Duration: duration})
}
