package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// mockTeamAuthorizationService is a mock implementation of TeamAuthorizationService
type mockTeamAuthorizationService struct {
	crossProfileDeniedCalled bool
	crossProfileDeniedParams struct {
		actorProfileID  string
		targetProfileID string
		operation       string
		metadata        map[string]interface{}
		clientIP        string
		correlationID   string
	}
}

func (m *mockTeamAuthorizationService) CrossTeamDenied(ctx context.Context, actorProfileID, targetProfileID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	m.crossProfileDeniedCalled = true
	m.crossProfileDeniedParams.actorProfileID = actorProfileID
	m.crossProfileDeniedParams.targetProfileID = targetProfileID
	m.crossProfileDeniedParams.operation = operation
	m.crossProfileDeniedParams.metadata = metadata
	m.crossProfileDeniedParams.clientIP = clientIP
	m.crossProfileDeniedParams.correlationID = correlationID
	return nil
}

func TestTeamAuthorizationServiceDelegatesToAuditService(t *testing.T) {
	adapter := NewTeamAuthorizationService(&mockAuditService{})
	assert.NotNil(t, adapter)
	assert.NoError(t, adapter.CrossTeamDenied(context.Background(), "actor-team", "target-team", "read", nil, "192.0.2.1", "corr-1"))
}

// TestProfileAuthorization_SameProfile_Allowed tests that standard principals can access their own profile.
func TestProfileAuthorization_SameProfile_Allowed(t *testing.T) {
	e := newTestEcho()
	mockAuthzSvc := &mockTeamAuthorizationService{}
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Set standard principal with matching profile
			ctx := c.Request().Context()
			ctx = context.WithValue(ctx, principalContextKey{}, &Principal{
				CredentialID: testUUIDPtr(uuid.New()),
				TeamID:       profileID,
				Role:         "standard",
				Grants:       []string{"read", "write"},
			})
			// Set same target profile in context
			ctx = context.WithValue(ctx, ResolvedTeamIDKey{}, profileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(AuthorizeTeam(mockAuthzSvc))

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called for same profile")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, mockAuthzSvc.crossProfileDeniedCalled, "CrossTeamDenied should not be called for same profile")
}

// TestProfileAuthorization_DifferentProfile_Forbidden tests that cross-profile access is denied.
func TestProfileAuthorization_DifferentProfile_Forbidden(t *testing.T) {
	e := newTestEcho()
	mockAuthzSvc := &mockTeamAuthorizationService{}
	actorProfileID := uuid.New()
	targetProfileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Set standard principal with different profile
			ctx := c.Request().Context()
			ctx = context.WithValue(ctx, principalContextKey{}, &Principal{
				CredentialID: testUUIDPtr(uuid.New()),
				TeamID:       actorProfileID,
				Role:         "standard",
				Grants:       []string{"read", "write"},
			})
			// Set different target profile in context
			ctx = context.WithValue(ctx, ResolvedTeamIDKey{}, targetProfileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(AuthorizeTeam(mockAuthzSvc))

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called for cross-profile access")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "FORBIDDEN")
	assert.True(t, mockAuthzSvc.crossProfileDeniedCalled, "CrossTeamDenied should be called")
	assert.Equal(t, actorProfileID.String(), mockAuthzSvc.crossProfileDeniedParams.actorProfileID)
	assert.Equal(t, targetProfileID.String(), mockAuthzSvc.crossProfileDeniedParams.targetProfileID)
}

// TestProfileAuthorization_NoTargetProfile_PassThrough tests that requests without a target profile pass through.
func TestProfileAuthorization_NoTargetProfile_PassThrough(t *testing.T) {
	e := newTestEcho()
	mockAuthzSvc := &mockTeamAuthorizationService{}
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Set standard principal
			ctx := c.Request().Context()
			ctx = context.WithValue(ctx, principalContextKey{}, &Principal{
				CredentialID: testUUIDPtr(uuid.New()),
				TeamID:       profileID,
				Role:         "standard",
				Grants:       []string{"read", "write"},
			})
			// No target profile set in context
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(AuthorizeTeam(mockAuthzSvc))

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called when no target profile")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, mockAuthzSvc.crossProfileDeniedCalled, "CrossTeamDenied should not be called")
}

// TestProfileAuthorization_NilPrincipal_Forbidden verifies the middleware fails
// closed when no principal is set (e.g., auth middleware misordered or bypassed).
func TestProfileAuthorization_NilPrincipal_Forbidden(t *testing.T) {
	e := newTestEcho()
	mockAuthzSvc := &mockTeamAuthorizationService{}
	targetProfileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Intentionally do NOT set a principal; simulate missing auth.
			ctx := c.Request().Context()
			ctx = context.WithValue(ctx, ResolvedTeamIDKey{}, targetProfileID)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(AuthorizeTeam(mockAuthzSvc))

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called when principal is nil")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "FORBIDDEN")
}

// TestRequireScopes_MatchingScopes_AllowsFullSet tests that callers with all
// required scopes are allowed through.
func TestRequireScopes_MatchingScopes_AllowsFullSet(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = context.WithValue(ctx, principalContextKey{}, &Principal{
				CredentialID: testUUIDPtr(uuid.New()),
				TeamID:       profileID,
				Role:         "standard",
				Grants:       []string{"read", "write", "delete"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(RequireScopes("read", "write", "delete"))

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called when all scopes are present")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireScopes_MatchingScopes_Allowed tests that principals with all required scopes are allowed.
func TestRequireScopes_MatchingScopes_Allowed(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = context.WithValue(ctx, principalContextKey{}, &Principal{
				CredentialID: testUUIDPtr(uuid.New()),
				TeamID:       profileID,
				Role:         "standard",
				Grants:       []string{"read", "write", "delete"},
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(RequireScopes("read", "write"))

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called when all scopes present")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireScopes_MissingScope_Forbidden tests that missing scopes result in 403.
func TestRequireScopes_MissingScope_Forbidden(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()
			ctx = context.WithValue(ctx, principalContextKey{}, &Principal{
				CredentialID: testUUIDPtr(uuid.New()),
				TeamID:       profileID,
				Role:         "standard",
				Grants:       []string{"read"}, // missing "write" and "delete"
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	})
	e.Use(RequireScopes("read", "write", "delete"))

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called when scopes missing")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "FORBIDDEN")
}
