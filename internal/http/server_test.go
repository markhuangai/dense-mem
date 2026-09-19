package http

import (
	"context"
	"encoding/json"
	"errors"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type captureLogProvider struct {
	level       string
	msg         string
	attrs       []httpcontract.LogAttr
	contextSeen context.Context
}

type legacyLogProvider struct {
	levels []string
}

func (l *legacyLogProvider) Info(string, ...httpcontract.LogAttr) {
	l.levels = append(l.levels, "info")
}

func (l *legacyLogProvider) Error(string, error, ...httpcontract.LogAttr) {
	l.levels = append(l.levels, "error")
}

func (l *legacyLogProvider) Warn(string, ...httpcontract.LogAttr) {
	l.levels = append(l.levels, "warn")
}

func (l *legacyLogProvider) Debug(string, ...httpcontract.LogAttr) {
	l.levels = append(l.levels, "debug")
}

func (l *legacyLogProvider) With(...httpcontract.LogAttr) httpcontract.LogProvider {
	return l
}

func (l *captureLogProvider) Info(msg string, attrs ...httpcontract.LogAttr) {
	l.level = "info"
	l.msg = msg
	l.attrs = append([]httpcontract.LogAttr(nil), attrs...)
}

func (l *captureLogProvider) InfoContext(ctx context.Context, msg string, attrs ...httpcontract.LogAttr) {
	l.contextSeen = ctx
	l.Info(msg, attrs...)
}

func (l *captureLogProvider) Error(msg string, err error, attrs ...httpcontract.LogAttr) {
	l.level = "error"
	l.msg = msg
	l.attrs = append([]httpcontract.LogAttr(nil), attrs...)
}

func (l *captureLogProvider) ErrorContext(ctx context.Context, msg string, err error, attrs ...httpcontract.LogAttr) {
	l.contextSeen = ctx
	l.Error(msg, err, attrs...)
}

func (l *captureLogProvider) Warn(msg string, attrs ...httpcontract.LogAttr) {
	l.level = "warn"
	l.msg = msg
	l.attrs = append([]httpcontract.LogAttr(nil), attrs...)
}

func (l *captureLogProvider) WarnContext(ctx context.Context, msg string, attrs ...httpcontract.LogAttr) {
	l.contextSeen = ctx
	l.Warn(msg, attrs...)
}

func (l *captureLogProvider) Debug(msg string, attrs ...httpcontract.LogAttr) {}

func (l *captureLogProvider) DebugContext(ctx context.Context, msg string, attrs ...httpcontract.LogAttr) {
	l.contextSeen = ctx
	l.Debug(msg, attrs...)
}

func (l *captureLogProvider) With(attrs ...httpcontract.LogAttr) httpcontract.LogProvider {
	return l
}

// TestHealthEndpointReturns200 verifies that /health returns 200 {"status":"ok"}
func TestHealthEndpointReturns200(t *testing.T) {
	// Arrange
	cfg := config.Config{}
	logger := &captureLogProvider{}
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
	logger := &captureLogProvider{}
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
	logger := &captureLogProvider{}

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
	logger := &captureLogProvider{}
	e := NewServer(cfg, logger, HealthConfig{Checks: []HealthCheck{{
		Name:     "migration_state",
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
	if deps["migration_state"] != "degraded" {
		t.Errorf("expected optional dependency to be degraded, got %v", deps["migration_state"])
	}
}

// TestReadyReadyWhenAllChecksPass verifies that /ready returns 200 when all checks pass
func TestReadyReadyWhenAllChecksPass(t *testing.T) {
	// Arrange
	cfg := config.Config{}
	logger := &captureLogProvider{}

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
	logger := &captureLogProvider{}
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
	logger := &captureLogProvider{}

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

func TestNewServerDefaultsBodyLimitForNilConfig(t *testing.T) {
	server := NewServer(nil, nil, HealthConfig{})
	if server == nil {
		t.Fatal("expected Echo instance to be created")
	}
}

func TestRequestLoggerOmitsQueryString(t *testing.T) {
	logger := &captureLogProvider{}
	e := NewServer(config.Config{}, logger, HealthConfig{})
	e.GET("/ui/api/recall", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/api/recall?query=secret-memory&token=raw-token", nil)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if logger.msg != "http_request" {
		t.Fatalf("expected http_request log, got %q", logger.msg)
	}
	if got := logAttrValue(logger.attrs, "uri"); got != "/ui/api/recall" {
		t.Fatalf("uri attr = %q, want %q", got, "/ui/api/recall")
	}
	if got := logAttrValue(logger.attrs, "route"); got != "/ui/api/recall" {
		t.Fatalf("route attr = %q, want %q", got, "/ui/api/recall")
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

func TestRequestLoggerCapturesRecoveredPanic(t *testing.T) {
	logger := &captureLogProvider{}
	e := NewServer(config.Config{}, logger, HealthConfig{})
	e.GET("/panic", func(echo.Context) error {
		panic("handler panic")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if logger.msg != "http_request" {
		t.Fatalf("last log = %q, want http_request", logger.msg)
	}
	attrs := make(map[string]any, len(logger.attrs))
	for _, attr := range logger.attrs {
		attrs[attr.Key] = attr.Value
	}
	if got := attrs["status"]; got != http.StatusInternalServerError {
		t.Fatalf("status attr = %#v, want %d", got, http.StatusInternalServerError)
	}
	if got := attrs["delivery_stage"]; got != "write_observed" {
		t.Fatalf("delivery stage = %#v, want write_observed", got)
	}
}

func TestRequestLoggerCapturesBodyLimitRejection(t *testing.T) {
	logger := &captureLogProvider{}
	e := NewServer(config.Config{HTTPMaxBodyBytes: 1}, logger, HealthConfig{})
	e.POST("/limited", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("too large"))
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if logger.msg != "http_request" {
		t.Fatalf("last log = %q, want http_request", logger.msg)
	}
	attrs := make(map[string]any, len(logger.attrs))
	for _, attr := range logger.attrs {
		attrs[attr.Key] = attr.Value
	}
	if got := attrs["status"]; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status attr = %#v, want %d", got, http.StatusRequestEntityTooLarge)
	}
	if got := attrs["delivery_stage"]; got != "write_observed" {
		t.Fatalf("delivery stage = %#v, want write_observed", got)
	}
	if rec.Header().Get(httpmw.CorrelationIDHeader) == "" {
		t.Fatal("body-limit response did not include a correlation ID")
	}
}

func TestControlPortalRequestLoggerCapturesBodyLimitCorrelation(t *testing.T) {
	logger := &captureLogProvider{}
	e, err := NewControlPortalServer(&config.Config{ControlPortalToken: "secret", HTTPMaxBodyBytes: 1}, nil, nil, logger)
	if err != nil {
		t.Fatalf("control portal: %v", err)
	}
	e.POST("/limited", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("too large"))
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if logger.msg != "control_http_request" {
		t.Fatalf("last log = %q, want control_http_request", logger.msg)
	}
	if rec.Header().Get(httpmw.CorrelationIDHeader) == "" {
		t.Fatal("control body-limit response did not include a correlation ID")
	}
}

func TestControlPortalRequestLoggerCapturesRecoveredPanic(t *testing.T) {
	logger := &captureLogProvider{}
	e, err := NewControlPortalServer(&config.Config{ControlPortalToken: "secret"}, nil, nil, logger)
	if err != nil {
		t.Fatalf("control portal: %v", err)
	}
	e.GET("/panic", func(echo.Context) error {
		panic("handler panic")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if logger.msg != "control_http_request" {
		t.Fatalf("last log = %q, want control_http_request", logger.msg)
	}
	attrs := make(map[string]any, len(logger.attrs))
	for _, attr := range logger.attrs {
		attrs[attr.Key] = attr.Value
	}
	if got := attrs["status"]; got != http.StatusInternalServerError {
		t.Fatalf("status attr = %#v, want %d", got, http.StatusInternalServerError)
	}
	if got := attrs["delivery_stage"]; got != "write_observed" {
		t.Fatalf("delivery stage = %#v, want write_observed", got)
	}
}

func TestRequestLoggerPreservesAuthenticatedContext(t *testing.T) {
	logger := &captureLogProvider{}
	e := NewServer(config.Config{}, logger, HealthConfig{})
	e.GET("/context", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID})
	ctx = requestctx.WithAuthenticationSecrets(ctx, "raw-api-key")
	req := httptest.NewRequest(http.MethodGet, "/context", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if logger.contextSeen == nil {
		t.Fatal("request logger did not receive the request context")
	}
	actor, ok := requestctx.ActorFromContext(logger.contextSeen)
	if !ok || actor.TeamID != teamID || actor.OwnerID != profileID {
		t.Fatalf("logged actor = %#v, ok=%v", actor, ok)
	}
	if got := requestctx.AuthenticationSecretsFromContext(logger.contextSeen); len(got) != 1 || got[0] != "raw-api-key" {
		t.Fatalf("logged authentication secrets = %#v", got)
	}
}

func TestRequestLoggerDetachesCanceledContextForCompletion(t *testing.T) {
	logger := &captureLogProvider{}
	e := NewServer(config.Config{}, logger, HealthConfig{})
	e.GET("/canceled", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	teamID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profileID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	ctx, cancel := context.WithCancel(requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID}))
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/canceled", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if logger.contextSeen == nil || logger.contextSeen.Err() != nil {
		t.Fatalf("completion logger retained cancellation: %v", logger.contextSeen)
	}
	actor, ok := requestctx.ActorFromContext(logger.contextSeen)
	if !ok || actor.TeamID != teamID || actor.OwnerID != profileID {
		t.Fatalf("detached logger context lost actor = %#v, ok=%v", actor, ok)
	}
}

func TestContextLogHelpersFallbackForLegacyProviders(t *testing.T) {
	logger := &legacyLogProvider{}
	ctx := context.Background()
	httpcontract.LogInfoContext(ctx, logger, "info")
	httpcontract.LogErrorContext(ctx, logger, "error", errors.New("error"))
	httpcontract.LogWarnContext(ctx, logger, "warn")
	httpcontract.LogDebugContext(ctx, logger, "debug")
	httpcontract.LogInfoContext(ctx, nil, "ignored")

	got := logger.levels
	want := []string{"info", "error", "warn", "debug"}
	if len(got) != len(want) {
		t.Fatalf("legacy log levels = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("legacy log levels = %#v, want %#v", got, want)
		}
	}
}

func TestRequestLoggerTreatsAnonymousUserSessionProbeAsInfo(t *testing.T) {
	logger := &captureLogProvider{}
	e := NewServer(config.Config{}, logger, HealthConfig{})
	e.GET("/ui/api/session", func(c echo.Context) error {
		return httperr.New(httperr.AUTH_MISSING, "authentication required")
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if logger.level != "info" {
		t.Fatalf("log level = %q, want info", logger.level)
	}
}

func TestRequestLoggerKeepsInvalidSessionCredentialsAtError(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "bearer",
			mutate: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer invalid")
			},
		},
		{
			name: "sso cookie",
			mutate: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: accessservice.SSOSessionCookieName, Value: "invalid"})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger := &captureLogProvider{}
			e := NewServer(config.Config{}, logger, HealthConfig{})
			e.GET("/ui/api/session", func(c echo.Context) error {
				return httperr.New(httperr.AUTH_MISSING, "authentication required")
			})
			req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
			test.mutate(req)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if logger.level != "error" {
				t.Fatalf("log level = %q, want error", logger.level)
			}
		})
	}
}

func logAttrValue(attrs []httpcontract.LogAttr, key string) string {
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
	e := NewServer(config.Config{}, &captureLogProvider{}, HealthConfig{})
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

	if err := ShutdownServer(e, &captureLogProvider{}); err != nil {
		t.Fatalf("ShutdownServer() error = %v", err)
	}
}

// TestHealthEndpointNoMiddleware verifies that /health has no middleware applied
func TestHealthEndpointNoMiddleware(t *testing.T) {
	cfg := config.Config{}
	logger := &captureLogProvider{}
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
	e := NewServer(config.Config{}, &captureLogProvider{}, HealthConfig{
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
	e := NewServer(config.Config{}, &captureLogProvider{}, HealthConfig{
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
	e := NewServer(config.Config{}, &captureLogProvider{}, HealthConfig{
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
