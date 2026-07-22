package http

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

func TestProtectedRoutesBlockDataPlaneWhenMigrationRequired(t *testing.T) {
	rawKey, repo := migrationGuardTestKey(t, []string{"read"})
	migration := &migrationDataPlaneStatusStub{status: &domain.V2MigrationControlStatus{
		DataPlaneAllowed: false,
		ReadinessMessage: "migration_required",
	}}

	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	called := false
	RegisterProtectedRoutesWithHandlers(e, ProtectedDeps{
		APIKeyRepo:      repo,
		MigrationStatus: migration,
	}, ProtectedHandlers{
		OpenAPIFull: func(c echo.Context) error {
			called = true
			return c.NoContent(nethttp.StatusNoContent)
		},
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/openapi.json", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusServiceUnavailable, rec.Body.String())
	}
	if called {
		t.Fatal("handler ran while migration gate was closed")
	}
	if repo.touched(repo.key.ID) {
		t.Fatal("last-used timestamp was updated while migration gate was closed")
	}
	assertMigrationGateError(t, rec.Body.Bytes(), "migration_required")
}

func TestUserPortalBlocksDataPlaneWhenMigrationRequired(t *testing.T) {
	rawKey, repo := migrationGuardTestKey(t, []string{"read"})
	migration := &migrationDataPlaneStatusStub{status: &domain.V2MigrationControlStatus{
		DataPlaneAllowed: false,
		ReadinessMessage: "migration_in_progress",
	}}

	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:      repo,
		MigrationStatus: migration,
	})

	req := httptest.NewRequest(nethttp.MethodGet, "/ui/api/session", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusServiceUnavailable, rec.Body.String())
	}
	assertMigrationGateError(t, rec.Body.Bytes(), "migration_in_progress")
}

func TestMigrationDataPlaneGateFailsClosedOnMissingStatus(t *testing.T) {
	migration := &migrationDataPlaneStatusStub{}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/gated", func(c echo.Context) error {
		return c.NoContent(nethttp.StatusNoContent)
	}, migrationDataPlaneGate(migration))

	req := httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusServiceUnavailable, rec.Body.String())
	}
	assertMigrationGateError(t, rec.Body.Bytes(), "migration state unavailable")
}

func TestMigrationDataPlaneGateAllowsNilProvider(t *testing.T) {
	e := echo.New()
	called := false
	e.GET("/gated", func(c echo.Context) error {
		called = true
		return c.NoContent(nethttp.StatusNoContent)
	}, migrationDataPlaneGate(nil))

	req := httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusNoContent, rec.Body.String())
	}
	if !called {
		t.Fatal("handler did not run")
	}
}

func TestMigrationDataPlaneGateAllowsOpenDataPlane(t *testing.T) {
	migration := &migrationDataPlaneStatusStub{status: &domain.V2MigrationControlStatus{DataPlaneAllowed: true}}
	e := echo.New()
	called := false
	e.GET("/gated", func(c echo.Context) error {
		called = true
		return c.NoContent(nethttp.StatusNoContent)
	}, migrationDataPlaneGate(migration))

	req := httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusNoContent, rec.Body.String())
	}
	if !called {
		t.Fatal("handler did not run")
	}
}

func TestMigrationDataPlaneGateCachesStatusWithinTTL(t *testing.T) {
	migration := &migrationDataPlaneStatusStub{status: &domain.V2MigrationControlStatus{DataPlaneAllowed: true}}
	e := echo.New()
	called := 0
	e.GET("/gated", func(c echo.Context) error {
		called++
		return c.NoContent(nethttp.StatusNoContent)
	}, migrationDataPlaneGateWithTTL(migration, time.Hour))

	req := httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusNoContent {
		t.Fatalf("first status = %d, want %d; body=%s", rec.Code, nethttp.StatusNoContent, rec.Body.String())
	}

	migration.status = &domain.V2MigrationControlStatus{
		DataPlaneAllowed: false,
		ReadinessMessage: "migration_required",
	}
	req = httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusNoContent {
		t.Fatalf("cached status = %d, want %d; body=%s", rec.Code, nethttp.StatusNoContent, rec.Body.String())
	}
	if called != 2 {
		t.Fatalf("handler calls = %d, want 2", called)
	}
	if migration.calls != 1 {
		t.Fatalf("status calls = %d, want 1", migration.calls)
	}
}

func TestMigrationDataPlaneGateDoesNotCacheCanceledStatusRefresh(t *testing.T) {
	migration := &migrationDataPlaneStatusStub{err: context.Canceled}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/gated", func(c echo.Context) error {
		return c.NoContent(nethttp.StatusNoContent)
	}, migrationDataPlaneGateWithTTL(migration, time.Hour))

	req := httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("first status = %d, want %d; body=%s", rec.Code, nethttp.StatusServiceUnavailable, rec.Body.String())
	}

	migration.err = nil
	migration.status = &domain.V2MigrationControlStatus{DataPlaneAllowed: true}
	req = httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusNoContent {
		t.Fatalf("second status = %d, want %d; body=%s", rec.Code, nethttp.StatusNoContent, rec.Body.String())
	}
	if migration.calls != 2 {
		t.Fatalf("status calls = %d, want 2", migration.calls)
	}
}

func TestMigrationDataPlaneGateFailsClosedOnStatusError(t *testing.T) {
	migration := &migrationDataPlaneStatusStub{err: errors.New("store unavailable")}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/gated", func(c echo.Context) error {
		return c.NoContent(nethttp.StatusNoContent)
	}, migrationDataPlaneGate(migration))

	req := httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusServiceUnavailable, rec.Body.String())
	}
	assertMigrationGateError(t, rec.Body.Bytes(), "migration state unavailable")
}

func TestMigrationDataPlaneGateUsesDefaultBlockedMessage(t *testing.T) {
	migration := &migrationDataPlaneStatusStub{status: &domain.V2MigrationControlStatus{DataPlaneAllowed: false}}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.GET("/gated", func(c echo.Context) error {
		return c.NoContent(nethttp.StatusNoContent)
	}, migrationDataPlaneGate(migration))

	req := httptest.NewRequest(nethttp.MethodGet, "/gated", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, nethttp.StatusServiceUnavailable, rec.Body.String())
	}
	assertMigrationGateError(t, rec.Body.Bytes(), "legacy migration is required; data plane is disabled")
}

func migrationGuardTestKey(t *testing.T, scopes []string) (string, *routerAPIKeyRepo) {
	t.Helper()
	rawKey, err := crypto.GenerateRawKey()
	if err != nil {
		t.Fatalf("GenerateRawKey: %v", err)
	}
	keyHash, err := crypto.HashKey(rawKey)
	if err != nil {
		t.Fatalf("HashKey: %v", err)
	}
	return rawKey, &routerAPIKeyRepo{key: &domain.APIKey{
		ID:        uuid.New(),
		TeamID:    uuid.New(),
		Name:      "migration-gated-client",
		KeyHash:   keyHash,
		KeyPrefix: crypto.GetKeyPrefix(rawKey),
		KeySuffix: crypto.GetKeySuffix(rawKey),
		Scopes:    scopes,
		RateLimit: 100,
		CreatedAt: time.Now().UTC(),
	}}
}

func assertMigrationGateError(t *testing.T, body []byte, message string) {
	t.Helper()
	var response struct {
		Code    httperr.ErrorCode `json:"code"`
		Message string            `json:"message"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, string(body))
	}
	if response.Code != httperr.SERVICE_UNAVAILABLE {
		t.Fatalf("code = %s, want %s", response.Code, httperr.SERVICE_UNAVAILABLE)
	}
	if response.Message != message {
		t.Fatalf("message = %q, want %q", response.Message, message)
	}
}

type migrationDataPlaneStatusStub struct {
	status *domain.V2MigrationControlStatus
	err    error
	calls  int
}

func (s *migrationDataPlaneStatusStub) Status(context.Context) (*domain.V2MigrationControlStatus, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.status, nil
}
