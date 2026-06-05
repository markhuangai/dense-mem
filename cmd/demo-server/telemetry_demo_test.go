package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewDemoTelemetryServer(t *testing.T) {
	scrapeHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# HELP densemem_test metric\n"))
	})
	server, err := newDemoTelemetryServer(scrapeHandler, "scrape-secret")
	require.NoError(t, err)
	require.Equal(t, 5, int(server.Server.ReadHeaderTimeout.Seconds()))
	require.Equal(t, 30, int(server.Server.ReadTimeout.Seconds()))
	require.Equal(t, 60, int(server.Server.IdleTimeout.Seconds()))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Telemetry-Scrape-Token", "wrong")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Telemetry-Scrape-Token", "scrape-secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "densemem_test")

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	_, err = newDemoTelemetryServer(scrapeHandler, "")
	require.ErrorContains(t, err, "demo telemetry scrape token is required")

	_, err = newDemoTelemetryServer(nil, "scrape-secret")
	require.ErrorContains(t, err, "demo telemetry scrape handler is required")
}
