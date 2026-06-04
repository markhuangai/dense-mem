package middleware

import (
	"context"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
)

// UsageMetricsMiddleware records authenticated request usage after API-key auth
// has derived team/key identity. Metrics are aggregated by route template, not raw path.
func UsageMetricsMiddleware(recorder service.UsageMetricsRecorder) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			recordUsageMetric(c, recorder, start, err)
			return err
		}
	}
}

// TelemetryHTTPMiddleware records authenticated HTTP request telemetry for
// scrape-oriented metrics backends such as Prometheus.
func TelemetryHTTPMiddleware(recorder observability.HTTPMetrics) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			recordTelemetryHTTPMetric(c, recorder, start, err)
			return err
		}
	}
}

func recordUsageMetric(c echo.Context, recorder service.UsageMetricsRecorder, start time.Time, err error) {
	if recorder == nil {
		return
	}
	ctx := c.Request().Context()
	principal := GetPrincipal(ctx)
	if principal == nil {
		return
	}
	route := c.Path()
	if route == "" {
		route = "unknown"
	}
	recorder.RecordRequest(context.Background(), domain.UsageMetricEvent{
		Timestamp: time.Now().UTC(),
		TeamID:    principal.GetTeamID(),
		KeyID:     principal.GetKeyID(),
		Method:    c.Request().Method,
		Route:     route,
		Status:    usageStatus(c, err),
		Latency:   time.Since(start),
	})
}

func recordTelemetryHTTPMetric(c echo.Context, recorder observability.HTTPMetrics, start time.Time, err error) {
	if recorder == nil {
		return
	}
	route := c.Path()
	if route == "" {
		route = "unknown"
	}
	recorder.ObserveHTTPRequest(c.Request().Context(), route, c.Request().Method, usageStatus(c, err), time.Since(start))
}

func usageStatus(c echo.Context, err error) int {
	if err == nil {
		if c.Response().Status != 0 {
			return c.Response().Status
		}
		return 200
	}
	if apiErr, ok := err.(*httperr.APIError); ok {
		return httperr.HTTPStatusCode(apiErr.Code)
	}
	if httpErr, ok := err.(*echo.HTTPError); ok {
		return httpErr.Code
	}
	if c.Response().Status != 0 {
		return c.Response().Status
	}
	return 500
}
