package middleware

import (
	"context"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/httperr"
	operations "github.com/markhuangai/dense-mem/internal/operations"
)

// UsageMetricsMiddleware records authenticated request usage after API-key auth
// has derived team/key identity. Metrics are aggregated by route template, not raw path.
func UsageMetricsMiddleware(recorder operations.UsageMetricsRecorder) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			requestContext, mcpMetrics := domain.WithMCPToolMetrics(c.Request().Context())
			c.SetRequest(c.Request().WithContext(requestContext))
			err := next(c)
			recordUsageMetric(c, recorder, start, err, mcpMetrics)
			return err
		}
	}
}

// TelemetryHTTPMiddleware records authenticated HTTP request telemetry for
// scrape-oriented metrics backends such as Prometheus.
func TelemetryHTTPMiddleware(recorder httpcontract.HTTPMetrics) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			recordTelemetryHTTPMetric(c, recorder, start, err)
			return err
		}
	}
}

func recordUsageMetric(c echo.Context, recorder operations.UsageMetricsRecorder, start time.Time, err error, mcpMetrics *domain.MCPToolMetrics) {
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
	mcpCalls, mcpFailures := mcpMetrics.Snapshot()
	recorder.RecordRequest(context.Background(), domain.UsageMetricEvent{
		Timestamp:       time.Now().UTC(),
		TeamID:          principal.GetTeamID(),
		KeyID:           principal.GetOwnerID(),
		Method:          c.Request().Method,
		Route:           route,
		Status:          usageStatus(c, err),
		Latency:         time.Since(start),
		MCPToolCalls:    mcpCalls,
		MCPToolFailures: mcpFailures,
	})
}

func recordTelemetryHTTPMetric(c echo.Context, recorder httpcontract.HTTPMetrics, start time.Time, err error) {
	if recorder == nil {
		return
	}
	route := c.Path()
	if route == "" {
		route = "unknown"
	}
	ctx := c.Request().Context()
	duration := time.Since(start)
	status := usageStatus(c, err)
	recorder.ObserveHTTPRequest(ctx, route, c.Request().Method, status, duration)
	if !isMCPRoute(route) {
		return
	}
	if mcpRecorder, ok := recorder.(interface {
		ObserveMCPTransportRequest(string, int, time.Duration)
		ObserveMCPToolResult(string, int64)
	}); ok {
		mcpRecorder.ObserveMCPTransportRequest(c.Request().Method, status, duration)
		for _, outcome := range domain.MCPToolMetricsFromContext(ctx).OutcomeSnapshot() {
			mcpRecorder.ObserveMCPToolResult(outcome.Outcome, outcome.Count)
		}
	}
}

func isMCPRoute(route string) bool {
	return route == "/mcp" || strings.HasSuffix(strings.TrimSuffix(route, "/"), "/mcp")
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
