package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlHTTPMetricsRecorder struct {
	events []controlHTTPMetricEvent
}

type controlHTTPMetricEvent struct {
	route    string
	method   string
	status   int
	duration time.Duration
}

func (r *controlHTTPMetricsRecorder) ObserveHTTPRequest(_ context.Context, route, method string, status int, duration time.Duration) {
	r.events = append(r.events, controlHTTPMetricEvent{route: route, method: method, status: status, duration: duration})
}

func TestControlPortalTelemetry(t *testing.T) {
	teamID := uuid.New()
	telemetry := &controlTelemetrySvc{snapshot: &service.TelemetrySnapshot{
		Available: true,
		Window:    service.TelemetryWindow{Key: "1h"},
		Scope:     service.TelemetryScope{Type: "team", TeamID: &teamID},
		Cards:     []service.TelemetryCard{{ID: "http_requests", Label: "HTTP requests", Unit: "requests", Value: 3}},
	}}
	scrapeHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# HELP densemem_test metric\n"))
	})
	httpMetrics := &controlHTTPMetricsRecorder{}
	e, err := NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		Reader:        telemetry,
		HTTPMetrics:   httpMetrics,
		ScrapeHandler: scrapeHandler,
		ScrapeToken:   "scrape-secret",
	}, HealthConfig{}, nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/control/api/telemetry?window=1h&scope=team&team_id="+teamID.String(), nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"available":true`)
	require.Equal(t, "1h", telemetry.filter.Window)
	require.Equal(t, "team", telemetry.filter.Scope)
	require.Equal(t, service.TelemetryAudienceOperator, telemetry.filter.Audience)
	require.Equal(t, teamID, *telemetry.filter.TeamID)
	require.Len(t, httpMetrics.events, 1)
	require.Equal(t, "/control/api/telemetry", httpMetrics.events[0].route)
	require.Equal(t, http.MethodGet, httpMetrics.events[0].method)
	require.Equal(t, http.StatusOK, httpMetrics.events[0].status)
	require.GreaterOrEqual(t, httpMetrics.events[0].duration, time.Duration(0))

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Telemetry-Scrape-Token", "scrape-secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "densemem_test")

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-secret")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	_, err = NewControlPortalServerWithMetricsAndTelemetry(&config.Config{
		ControlHTTPAddr:    "127.0.0.1:8090",
		ControlPortalToken: "secret",
	}, &controlProfileSvc{}, &controlKeySvc{}, nil, ControlPortalTelemetry{
		ScrapeHandler: scrapeHandler,
	}, HealthConfig{}, nil)
	require.ErrorContains(t, err, "telemetry scrape token is required")
}
