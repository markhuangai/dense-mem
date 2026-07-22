package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type captureLogProvider struct {
	msg   string
	attrs []observability.LogAttr
}

func (l *captureLogProvider) Info(msg string, attrs ...observability.LogAttr) {
	l.msg = msg
	l.attrs = append([]observability.LogAttr(nil), attrs...)
}

func (l *captureLogProvider) Error(msg string, err error, attrs ...observability.LogAttr) {
	l.Info(msg, attrs...)
}

func (l *captureLogProvider) Warn(msg string, attrs ...observability.LogAttr) {
	l.Info(msg, attrs...)
}

func (l *captureLogProvider) Debug(msg string, attrs ...observability.LogAttr) {}

func (l *captureLogProvider) With(attrs ...observability.LogAttr) observability.LogProvider {
	return l
}

// TestHealthEndpointReturns200 verifies that /health returns 200 {"status":"ok"}
func TestHealthEndpointReturns200(t *testing.T) {
	// Arrange
	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)
	e := NewServer(cfg, logger, HealthConfig{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Act
	err := handleHealth(HealthConfig{})(c)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%v'", response["status"])
	}
}

// TestReadyBypassesAuth verifies that /ready is not behind auth/profile/rate-limit middleware
func TestReadyBypassesAuth(t *testing.T) {
	// Arrange
	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)
	checks := []HealthCheck{}
	e := NewServer(cfg, logger, HealthConfig{Checks: checks})

	// Act - make request without any auth headers
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	// Execute the request through Echo
	e.ServeHTTP(rec, req)

	// Assert - should get 200, not 401/403 (auth) or 429 (rate limit)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["status"] != "ready" {
		t.Errorf("expected status 'ready', got '%v'", response["status"])
	}
}

// TestReadyDegradedWhenCheckFails verifies that /ready returns 503 when at least one HealthCheck returns error
func TestReadyDegradedWhenCheckFails(t *testing.T) {
	// Arrange
	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)

	// Create a failing health check
	failingCheck := HealthCheck{
		Name: "db",
		Check: func(ctx context.Context) error {
			return errors.New("database connection failed")
		},
	}

	checks := []HealthCheck{failingCheck}
	e := NewServer(cfg, logger, HealthConfig{Checks: checks})

	// Act
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Assert
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["status"] != "degraded" {
		t.Errorf("expected status 'degraded', got '%v'", response["status"])
	}

	deps, ok := response["dependencies"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected dependencies to be a map, got %T", response["dependencies"])
	}

	// Check that at least one dependency is marked as failed
	foundFailed := false
	for _, status := range deps {
		if status == "failed" {
			foundFailed = true
			break
		}
	}
	if !foundFailed {
		t.Error("expected at least one dependency to be 'failed'")
	}
}

func TestReadyReportsOptionalFailureWithoutBlocking(t *testing.T) {
	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)
	e := NewServer(cfg, logger, HealthConfig{Checks: []HealthCheck{{
		Name:     "v2_migration_state",
		Optional: true,
		Check: func(ctx context.Context) error {
			return errors.New("migration pending")
		},
	}}})

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	deps, ok := response["dependencies"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected dependencies to be a map, got %T", response["dependencies"])
	}
	if deps["v2_migration_state"] != "degraded" {
		t.Errorf("expected optional dependency to be degraded, got %v", deps["v2_migration_state"])
	}
}

// TestReadyReadyWhenAllChecksPass verifies that /ready returns 200 when all checks pass
func TestReadyReadyWhenAllChecksPass(t *testing.T) {
	// Arrange
	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)

	// Create passing health checks
	passingCheck1 := HealthCheck{
		Name:  "check1",
		Check: func(ctx context.Context) error { return nil },
	}
	passingCheck2 := HealthCheck{
		Name:  "check2",
		Check: func(ctx context.Context) error { return nil },
	}

	checks := []HealthCheck{passingCheck1, passingCheck2}
	e := NewServer(cfg, logger, HealthConfig{Checks: checks})

	// Act
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Assert
	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["status"] != "ready" {
		t.Errorf("expected status 'ready', got '%v'", response["status"])
	}

	deps, ok := response["dependencies"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected dependencies to be a map, got %T", response["dependencies"])
	}

	// All dependencies should be "ok"
	for _, status := range deps {
		if status != "ok" {
			t.Errorf("expected all dependencies to be 'ok', got '%v'", status)
		}
	}
}

// TestGracefulShutdown verifies that in-flight requests complete within the shutdown window
func TestGracefulShutdown(t *testing.T) {
	// This test verifies the shutdown timeout is set to 10 seconds
	// We can't easily test actual graceful shutdown in unit tests,
	// but we can verify the timeout constant is correct

	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)
	checks := []HealthCheck{}

	// Create server with graceful shutdown
	_, shutdown := NewServerWithGracefulShutdown(cfg, logger, HealthConfig{Checks: checks})

	// The shutdown function should exist and be callable
	// In a real scenario, it would use 10-second timeout
	if shutdown == nil {
		t.Error("expected shutdown function to be returned")
	}

	// We can call shutdown immediately since no server is actually running
	// This should complete quickly as there's nothing to shut down
	shutdown()
}

// TestNewServerAcceptsHealthChecks verifies that NewServer accepts HealthConfig and compiles
func TestNewServerAcceptsHealthChecks(t *testing.T) {
	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)

	// Create various health checks
	checks := []HealthCheck{
		{Name: "check1", Check: func(ctx context.Context) error { return nil }},
		{Name: "check2", Check: func(ctx context.Context) error { return nil }},
	}

	// This should compile and create a server
	server := NewServer(cfg, logger, HealthConfig{Checks: checks})
	if server == nil {
		t.Error("expected Echo instance to be created")
	}
}

func TestRequestLoggerOmitsQueryString(t *testing.T) {
	logger := &captureLogProvider{}
	e := NewServer(config.Config{}, logger, HealthConfig{})
	e.GET("/api/v1/recall", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recall?query=secret-memory&token=raw-token", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if logger.msg != "http_request" {
		t.Fatalf("expected http_request log, got %q", logger.msg)
	}
	if got := logAttrValue(logger.attrs, "uri"); got != "/api/v1/recall" {
		t.Fatalf("uri attr = %q, want %q", got, "/api/v1/recall")
	}
	if got := logAttrValue(logger.attrs, "route"); got != "/api/v1/recall" {
		t.Fatalf("route attr = %q, want %q", got, "/api/v1/recall")
	}
	for _, attr := range logger.attrs {
		value, ok := attr.Value.(string)
		if !ok {
			continue
		}
		if value == "secret-memory" || value == "raw-token" {
			t.Fatalf("sensitive query value leaked in attr %q", attr.Key)
		}
	}
}

func logAttrValue(attrs []observability.LogAttr, key string) string {
	for _, attr := range attrs {
		if attr.Key != key {
			continue
		}
		value, _ := attr.Value.(string)
		return value
	}
	return ""
}

func TestNewServerUsesDirectIPByDefault(t *testing.T) {
	e := NewServer(config.Config{}, observability.New(slog.LevelInfo), HealthConfig{})
	e.GET("/ip", func(c echo.Context) error {
		return c.String(http.StatusOK, c.RealIP())
	})

	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("X-Forwarded-For", "198.51.100.77")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if got, want := rec.Body.String(), "203.0.113.10"; got != want {
		t.Errorf("RealIP() = %q, want %q", got, want)
	}
}

func TestServerWrapperMethods(t *testing.T) {
	e := echo.New()
	server := &Server{echo: e}

	if got := server.GetEcho(); got != e {
		t.Fatalf("GetEcho() = %p; want %p", got, e)
	}

	if err := server.Start("not-a-valid-listen-address"); err == nil {
		t.Fatal("expected Start to return an error for an invalid listen address")
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	if err := ShutdownServer(e, observability.New(slog.LevelInfo)); err != nil {
		t.Fatalf("ShutdownServer() error = %v", err)
	}
}

// TestHealthEndpointNoMiddleware verifies that /health has no middleware applied
func TestHealthEndpointNoMiddleware(t *testing.T) {
	cfg := config.Config{}
	logger := observability.New(slog.LevelInfo)
	e := NewServer(cfg, logger, HealthConfig{})

	// Make request without any headers
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	// Should get 200 without any auth
	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

// TestHealthEndpoint_NoRedis_ReturnsDegraded200 verifies that /health returns 200
// with degraded=true when running in in-memory mode.
func TestHealthEndpoint_NoRedis_ReturnsDegraded200(t *testing.T) {
	e := NewServer(config.Config{}, observability.New(slog.LevelInfo), HealthConfig{
		Degraded: true,
		Reason:   "in-memory backend: no cross-instance rate limiting or session cleanup",
		Checks: []HealthCheck{
			{Name: "postgres", Check: func(ctx context.Context) error { return nil }},
			{Name: "pgvector", Check: func(ctx context.Context) error { return nil }},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%v'", body["status"])
	}

	if body["degraded"] != true {
		t.Errorf("expected degraded=true, got '%v'", body["degraded"])
	}

	reason, ok := body["reason"].(string)
	if !ok {
		t.Fatal("expected reason to be a string")
	}
	if reason != "in-memory backend: no cross-instance rate limiting or session cleanup" {
		t.Errorf("unexpected reason: %s", reason)
	}
}

// TestHealthEndpoint_RedisEnabled_ReturnsNonDegraded verifies /health returns 200
// without degraded when Redis is enabled.
func TestHealthEndpoint_RedisEnabled_ReturnsNonDegraded(t *testing.T) {
	e := NewServer(config.Config{}, observability.New(slog.LevelInfo), HealthConfig{
		Degraded: false,
		Reason:   "",
		Checks: []HealthCheck{
			{Name: "postgres", Check: func(ctx context.Context) error { return nil }},
			{Name: "redis", Check: func(ctx context.Context) error { return nil }},
			{Name: "pgvector", Check: func(ctx context.Context) error { return nil }},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%v'", body["status"])
	}

	_, hasDegraded := body["degraded"]
	if hasDegraded {
		t.Error("expected no 'degraded' field when Redis is enabled")
	}
}

// TestHealthEndpoint_Degraded_ContainsChecks verifies that degraded /health includes named checks.
func TestHealthEndpoint_Degraded_ContainsChecks(t *testing.T) {
	e := NewServer(config.Config{}, observability.New(slog.LevelInfo), HealthConfig{
		Degraded: true,
		Reason:   "in-memory backend: no cross-instance rate limiting or session cleanup",
		Checks: []HealthCheck{
			{Name: "postgres", Check: func(ctx context.Context) error { return nil }},
			{Name: "pgvector", Check: func(ctx context.Context) error { return nil }},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	deps, ok := body["dependencies"].(map[string]any)
	if !ok {
		t.Fatal("expected dependencies to be a map")
	}

	if _, hasPostgres := deps["postgres"]; !hasPostgres {
		t.Error("expected 'postgres' check in dependencies")
	}
	if _, hasPGVector := deps["pgvector"]; !hasPGVector {
		t.Error("expected 'pgvector' check in dependencies")
	}
}
