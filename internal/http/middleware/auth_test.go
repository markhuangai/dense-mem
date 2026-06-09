package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service"
)

// mockAPIKeyRepository is a mock implementation of repository.APIKeyRepository
type mockAPIKeyRepository struct {
	getActiveByPrefixFunc func(ctx context.Context, prefix string) (*domain.APIKey, error)
	touchLastUsedFunc     func(ctx context.Context, id uuid.UUID) error
	touchLastUsedCalled   bool
	touchLastUsedID       uuid.UUID
	touchLastUsedMu       sync.Mutex
}

func (m *mockAPIKeyRepository) CreateStandardKey(ctx context.Context, key *domain.APIKey) error {
	return nil
}

func (m *mockAPIKeyRepository) ListByProfile(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.APIKey, error) {
	return nil, nil
}

func (m *mockAPIKeyRepository) GetActiveByPrefix(ctx context.Context, prefix string) (*domain.APIKey, error) {
	if m.getActiveByPrefixFunc != nil {
		return m.getActiveByPrefixFunc(ctx, prefix)
	}
	return nil, nil
}

func (m *mockAPIKeyRepository) RevokeForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	return 0, nil
}

func (m *mockAPIKeyRepository) RotateForProfile(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error) {
	return 0, nil
}

func (m *mockAPIKeyRepository) UpdateNameForProfile(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error) {
	return 0, nil
}

func (m *mockAPIKeyRepository) UpdateRoleForProfile(ctx context.Context, profileID, id uuid.UUID, role string) (int64, error) {
	return 0, nil
}

func (m *mockAPIKeyRepository) DeleteForProfile(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	return 0, nil
}

func (m *mockAPIKeyRepository) GetByIDForProfile(ctx context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}

func (m *mockAPIKeyRepository) CountByProfile(ctx context.Context, profileID uuid.UUID) (int64, error) {
	return 0, nil
}

func (m *mockAPIKeyRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	m.touchLastUsedMu.Lock()
	m.touchLastUsedCalled = true
	m.touchLastUsedID = id
	m.touchLastUsedMu.Unlock()
	if m.touchLastUsedFunc != nil {
		return m.touchLastUsedFunc(ctx, id)
	}
	return nil
}

// mockAuditService is a mock implementation of service.AuditService
type mockAuditService struct {
	authFailureCalled bool
	authFailureParams struct {
		profileID     *string
		entityType    string
		entityID      string
		metadata      map[string]interface{}
		clientIP      string
		correlationID string
	}
}

func (m *mockAuditService) Append(ctx context.Context, entry service.AuditLogEntry) error {
	return nil
}

func (m *mockAuditService) ProfileCreated(ctx context.Context, profileID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) ProfileUpdated(ctx context.Context, profileID string, beforePayload, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) ProfileDeleteBlocked(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string, reason string) error {
	return nil
}

func (m *mockAuditService) ProfileDeleted(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) APIKeyCreated(ctx context.Context, profileID *string, keyID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) APIKeyRevoked(ctx context.Context, profileID *string, keyID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) AuthFailure(ctx context.Context, profileID *string, entityType, entityID string, metadata map[string]interface{}, clientIP, correlationID string) error {
	m.authFailureCalled = true
	m.authFailureParams.profileID = profileID
	m.authFailureParams.entityType = entityType
	m.authFailureParams.entityID = entityID
	m.authFailureParams.metadata = metadata
	m.authFailureParams.clientIP = clientIP
	m.authFailureParams.correlationID = correlationID
	return nil
}

func (m *mockAuditService) CrossProfileDenied(ctx context.Context, actorProfileID, targetProfileID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) RateLimited(ctx context.Context, profileID *string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) SystemQuery(ctx context.Context, queryType string, metadata map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) InvariantViolation(ctx context.Context, entityType, entityID string, violation string, metadata map[string]interface{}, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) List(ctx context.Context, profileID string, limit, offset int) ([]service.AuditLogEntry, int, error) {
	return []service.AuditLogEntry{}, 0, nil
}

type mockSecurityService struct {
	recordAuthFailureCalled  bool
	recordAuthFailureIP      string
	recordAuthFailureSurface string
	recordAuthFailureReason  string
}

func (m *mockSecurityService) CheckBan(ctx context.Context, ip string) (*domain.SecurityIPBan, error) {
	return nil, nil
}

func (m *mockSecurityService) RecordAuthFailure(ctx context.Context, ip, surface, reason string) (*domain.SecurityIPBan, error) {
	m.recordAuthFailureCalled = true
	m.recordAuthFailureIP = ip
	m.recordAuthFailureSurface = surface
	m.recordAuthFailureReason = reason
	return nil, nil
}

type mockSSOEntitlementValidator struct {
	validateFunc func(ctx context.Context, key *domain.APIKey) (*domain.APIKey, error)
}

func (m mockSSOEntitlementValidator) ValidateAPIKeyPrincipal(ctx context.Context, key *domain.APIKey) (*domain.APIKey, error) {
	return m.validateFunc(ctx, key)
}

type mockSSOSessionAuthenticator struct {
	authenticateFunc func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error)
}

func (m mockSSOSessionAuthenticator) AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error) {
	return m.authenticateFunc(ctx, sessionToken, csrfToken, requireCSRF)
}

// newTestEcho creates a new Echo instance with the custom error handler
func newTestEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	return e
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	e := newTestEcho()
	mockRepo := &mockAPIKeyRepository{}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Verify error response contains AUTH_MISSING
	bodyStr := rec.Body.String()
	assert.Contains(t, bodyStr, "AUTH_MISSING")
	assert.True(t, mockAudit.authFailureCalled, "audit service should be called for auth failure")
}

func TestAuthMiddleware_WithSecurityStillAuditsAuthFailure(t *testing.T) {
	e := newTestEcho()
	mockRepo := &mockAPIKeyRepository{}
	mockAudit := &mockAuditService{}
	mockSecurity := &mockSecurityService{}

	e.Use(AuthMiddlewareWithSecurity(mockRepo, mockAudit, mockSecurity))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "192.0.2.10:12345"
	rec := httptest.NewRecorder()
	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.True(t, mockSecurity.recordAuthFailureCalled, "security service should record auth failure")
	assert.True(t, mockAudit.authFailureCalled, "audit service should still record auth failure")
	assert.Equal(t, "192.0.2.10", mockSecurity.recordAuthFailureIP)
	assert.Equal(t, "api", mockSecurity.recordAuthFailureSurface)
	assert.Equal(t, "AUTH_MISSING", mockSecurity.recordAuthFailureReason)
}

func TestAuthMiddleware_MalformedHeader(t *testing.T) {
	e := newTestEcho()
	mockRepo := &mockAPIKeyRepository{}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	testCases := []struct {
		name          string
		authHeader    string
		expectedError string
	}{
		{
			name:          "no bearer prefix",
			authHeader:    "InvalidToken",
			expectedError: "AUTH_INVALID",
		},
		{
			name:          "wrong scheme",
			authHeader:    "Basic dXNlcjpwYXNz",
			expectedError: "AUTH_INVALID",
		},
		{
			name:          "empty bearer token",
			authHeader:    "Bearer ",
			expectedError: "AUTH_INVALID",
		},
		{
			name:          "only bearer prefix",
			authHeader:    "Bearer",
			expectedError: "AUTH_INVALID",
		},
		{
			name:          "key too short",
			authHeader:    "Bearer short",
			expectedError: "AUTH_INVALID",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockAudit.authFailureCalled = false

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", tc.authHeader)
			rec := httptest.NewRecorder()

			handlerCalled := false
			e.GET("/test", func(c echo.Context) error {
				handlerCalled = true
				return c.String(http.StatusOK, "ok")
			})

			e.ServeHTTP(rec, req)

			assert.False(t, handlerCalled, "handler should not be called")
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.expectedError)
			assert.True(t, mockAudit.authFailureCalled, "audit service should be called for auth failure")
		})
	}
}

func TestAuthMiddleware_NoMatchingKey(t *testing.T) {
	e := newTestEcho()
	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return nil, nil // No key found
		},
	}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "AUTH_INVALID")
	assert.True(t, mockAudit.authFailureCalled, "audit service should be called for auth failure")
}

func TestAuthMiddleware_RevokedKey(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	keyID := uuid.New()
	revokedAt := time.Now().UTC().Add(-time.Hour)

	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return &domain.APIKey{
				ID:        keyID,
				ProfileID: profileID,
				KeyHash:   "testprefix12345678901234567890",
				Scopes:    []string{"read"},
				RevokedAt: &revokedAt,
			}, nil
		},
	}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "AUTH_REVOKED")
	assert.True(t, mockAudit.authFailureCalled, "audit service should be called for auth failure")
}

func TestAuthMiddleware_ExpiredKey(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	keyID := uuid.New()
	expiresAt := time.Now().UTC().Add(-time.Hour)

	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return &domain.APIKey{
				ID:        keyID,
				ProfileID: profileID,
				KeyHash:   "testprefix12345678901234567890",
				Scopes:    []string{"read"},
				ExpiresAt: &expiresAt,
			}, nil
		},
	}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer testprefix12345678901234567890")
	rec := httptest.NewRecorder()

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "AUTH_EXPIRED")
	assert.True(t, mockAudit.authFailureCalled, "audit service should be called for auth failure")
}

func TestAuthMiddleware_ValidKey_StoresPrincipal(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	keyID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return &domain.APIKey{
				ID:        keyID,
				ProfileID: profileID,
				KeyHash:   keyHash,
				Scopes:    []string{"read", "write"},
				Role:      service.APIKeyRoleManager,
				RevokedAt: nil,
				ExpiresAt: nil,
			}, nil
		},
	}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	var capturedPrincipal *Principal
	e.GET("/test", func(c echo.Context) error {
		capturedPrincipal = GetPrincipal(c.Request().Context())

		// Verify Authorization header is removed
		authHeader := c.Request().Header.Get("Authorization")
		assert.Empty(t, authHeader, "Authorization header should be removed")

		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedPrincipal, "principal should be stored in context")
	assert.Equal(t, keyID, capturedPrincipal.KeyID)
	assert.Equal(t, profileID, capturedPrincipal.TeamID)
	require.NotNil(t, capturedPrincipal.ProfileID)
	assert.Equal(t, keyID, *capturedPrincipal.ProfileID)
	assert.Equal(t, service.APIKeyRoleManager, capturedPrincipal.Role)
	assert.Equal(t, []string{"read", "write"}, capturedPrincipal.Scopes)
	assert.Equal(t, rawKey[:24], capturedPrincipal.KeyPrefix)
}

func TestAuthMiddleware_SSOEntitlementValidatorOverridesPrincipal(t *testing.T) {
	e := newTestEcho()
	originalTeamID := uuid.New()
	mappedTeamID := uuid.New()
	originalProfileID := uuid.New()
	mappedProfileID := uuid.New()
	providerID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return &domain.APIKey{
				ID:        originalProfileID,
				ProfileID: originalTeamID,
				TeamID:    originalTeamID,
				Name:      "Original profile",
				TeamName:  "Original team",
				KeyHash:   keyHash,
				Scopes:    []string{"read"},
				Role:      service.APIKeyRoleMember,
				RateLimit: 10,
			}, nil
		},
	}
	validator := mockSSOEntitlementValidator{
		validateFunc: func(ctx context.Context, key *domain.APIKey) (*domain.APIKey, error) {
			require.Equal(t, originalProfileID, key.ID)
			validated := *key
			validated.ID = mappedProfileID
			validated.ProfileID = mappedTeamID
			validated.TeamID = mappedTeamID
			validated.Name = "Mapped profile"
			validated.TeamName = "Mapped team"
			validated.Scopes = []string{"read", "write"}
			validated.Role = service.APIKeyRoleManager
			validated.RateLimit = 77
			validated.SSOProviderID = &providerID
			validated.SSOSubject = "subject-123"
			return &validated, nil
		},
	}

	e.Use(AuthMiddlewareWithOptions(mockRepo, nil, nil, AuthOptions{SSOEntitlementValidator: validator}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	var capturedPrincipal *Principal
	var capturedActor requestctx.ActorProfile
	var actorOK bool
	e.GET("/test", func(c echo.Context) error {
		capturedPrincipal = GetPrincipal(c.Request().Context())
		capturedActor, actorOK = requestctx.ActorProfileFromContext(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedPrincipal)
	assert.Equal(t, mappedProfileID, capturedPrincipal.KeyID)
	assert.Equal(t, mappedTeamID, capturedPrincipal.TeamID)
	require.NotNil(t, capturedPrincipal.ProfileID)
	assert.Equal(t, mappedProfileID, *capturedPrincipal.ProfileID)
	assert.Equal(t, "Mapped profile", capturedPrincipal.ProfileName)
	assert.Equal(t, service.APIKeyRoleManager, capturedPrincipal.Role)
	assert.Equal(t, []string{"read", "write"}, capturedPrincipal.Scopes)
	assert.Equal(t, 77, capturedPrincipal.RateLimit)
	require.NotNil(t, capturedPrincipal.SSOProviderID)
	assert.Equal(t, providerID, *capturedPrincipal.SSOProviderID)
	assert.Equal(t, "subject-123", capturedPrincipal.SSOSubject)
	require.True(t, actorOK)
	assert.Equal(t, mappedTeamID, capturedActor.TeamID)
	assert.Equal(t, "Mapped team", capturedActor.TeamName)
	assert.Equal(t, mappedProfileID, capturedActor.ProfileID)
	assert.Equal(t, "Mapped profile", capturedActor.ProfileName)
}

func TestAuthMiddleware_SSOEntitlementValidatorDeniesKey(t *testing.T) {
	e := newTestEcho()
	teamID := uuid.New()
	keyID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return &domain.APIKey{
				ID:        keyID,
				ProfileID: teamID,
				TeamID:    teamID,
				KeyHash:   keyHash,
				Scopes:    []string{"read"},
				Role:      service.APIKeyRoleMember,
			}, nil
		},
	}
	mockAudit := &mockAuditService{}
	validator := mockSSOEntitlementValidator{
		validateFunc: func(ctx context.Context, key *domain.APIKey) (*domain.APIKey, error) {
			return nil, service.ErrSSOEntitlementRefreshStale
		},
	}

	e.Use(AuthMiddlewareWithOptions(mockRepo, mockAudit, nil, AuthOptions{SSOEntitlementValidator: validator}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	handlerCalled := false
	e.GET("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "FORBIDDEN")
	assert.True(t, mockAudit.authFailureCalled)
}

func TestAuthMiddleware_SSOSessionAuthenticatesWithoutAuthorizationHeader(t *testing.T) {
	e := newTestEcho()
	teamID := uuid.New()
	profileID := uuid.New()
	providerID := uuid.New()
	authenticator := mockSSOSessionAuthenticator{
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error) {
			require.Equal(t, "session-token", sessionToken)
			require.Empty(t, csrfToken)
			require.False(t, requireCSRF)
			return &domain.APIKey{
				ID:            profileID,
				ProfileID:     teamID,
				TeamID:        teamID,
				Name:          "SSO profile",
				TeamName:      "SSO team",
				Scopes:        []string{"read"},
				Role:          service.APIKeyRoleMember,
				RateLimit:     120,
				SSOProviderID: &providerID,
				SSOSubject:    "subject-123",
			}, nil
		},
	}

	e.Use(AuthMiddlewareWithOptions(&mockAPIKeyRepository{}, nil, nil, AuthOptions{SSOSessionAuthenticator: authenticator}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.AddCookie(&http.Cookie{Name: service.SSOSessionCookieName, Value: "session-token"})
	rec := httptest.NewRecorder()

	var capturedPrincipal *Principal
	e.GET("/test", func(c echo.Context) error {
		capturedPrincipal = GetPrincipal(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedPrincipal)
	assert.Equal(t, "sso_session", capturedPrincipal.AuthMethod)
	assert.Equal(t, profileID, capturedPrincipal.KeyID)
	assert.Equal(t, teamID, capturedPrincipal.TeamID)
	assert.Equal(t, []string{"read"}, capturedPrincipal.Scopes)
	require.NotNil(t, capturedPrincipal.SSOProviderID)
	assert.Equal(t, providerID, *capturedPrincipal.SSOProviderID)
}

func TestAuthMiddleware_SSOSessionRequiresCSRFHeaderForUnsafeMethods(t *testing.T) {
	e := newTestEcho()
	authenticator := mockSSOSessionAuthenticator{
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error) {
			require.Equal(t, "session-token", sessionToken)
			require.Empty(t, csrfToken)
			require.True(t, requireCSRF)
			return nil, service.ErrSSOCSRFInvalid
		},
	}
	e.Use(AuthMiddlewareWithOptions(&mockAPIKeyRepository{}, nil, nil, AuthOptions{SSOSessionAuthenticator: authenticator}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.AddCookie(&http.Cookie{Name: service.SSOSessionCookieName, Value: "session-token"})
	req.AddCookie(&http.Cookie{Name: service.SSOCSRFCookieName, Value: "cookie-csrf"})
	rec := httptest.NewRecorder()
	handlerCalled := false
	e.POST("/test", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid sso csrf token")
}

func TestAuthMiddleware_SSOSessionUsesCSRFHeaderForUnsafeMethods(t *testing.T) {
	e := newTestEcho()
	teamID := uuid.New()
	profileID := uuid.New()
	authenticator := mockSSOSessionAuthenticator{
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error) {
			require.Equal(t, "session-token", sessionToken)
			require.Equal(t, "header-csrf", csrfToken)
			require.True(t, requireCSRF)
			return &domain.APIKey{
				ID:        profileID,
				ProfileID: teamID,
				TeamID:    teamID,
				Name:      "SSO profile",
				Scopes:    []string{"read"},
				Role:      service.APIKeyRoleMember,
			}, nil
		},
	}
	e.Use(AuthMiddlewareWithOptions(&mockAPIKeyRepository{}, nil, nil, AuthOptions{SSOSessionAuthenticator: authenticator}))

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.AddCookie(&http.Cookie{Name: service.SSOSessionCookieName, Value: "session-token"})
	req.AddCookie(&http.Cookie{Name: service.SSOCSRFCookieName, Value: "cookie-csrf"})
	req.Header.Set(service.SSOCSRFHeaderName, "header-csrf")
	rec := httptest.NewRecorder()
	e.POST("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_LegacyPrefixLookupFallback(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	keyID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	var prefixes []string
	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			prefixes = append(prefixes, prefix)
			if prefix != rawKey[:12] {
				return nil, nil
			}
			return &domain.APIKey{
				ID:        keyID,
				ProfileID: profileID,
				KeyHash:   keyHash,
				Scopes:    []string{"read"},
			}, nil
		},
	}

	e.Use(AuthMiddleware(mockRepo, nil))
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, prefixes, 2)
	assert.Equal(t, rawKey[:24], prefixes[0])
	assert.Equal(t, rawKey[:12], prefixes[1])
}

func TestAuthMiddleware_ProfilelessKeyRejected(t *testing.T) {
	e := newTestEcho()
	keyID := uuid.New()
	rawKey := "legacykey12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return &domain.APIKey{
				ID:        keyID,
				ProfileID: uuid.Nil,
				KeyHash:   keyHash,
				Scopes:    []string{"read"},
				RevokedAt: nil,
				ExpiresAt: nil,
			}, nil
		},
	}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "AUTH_INVALID")
}

func TestAuthMiddleware_DoesNotTouchLastUsed(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	keyID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	mockRepo := &mockAPIKeyRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.APIKey, error) {
			return &domain.APIKey{
				ID:        keyID,
				ProfileID: profileID,
				KeyHash:   keyHash,
				Scopes:    []string{"read"},
				RevokedAt: nil,
				ExpiresAt: nil,
			}, nil
		},
	}
	mockAudit := &mockAuditService{}

	e.Use(AuthMiddleware(mockRepo, mockAudit))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()

	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	mockRepo.touchLastUsedMu.Lock()
	assert.False(t, mockRepo.touchLastUsedCalled, "auth middleware should not touch last used")
	mockRepo.touchLastUsedMu.Unlock()
}

func TestLastUsedMiddleware_TouchLastUsed_Background(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	keyID := uuid.New()
	mockRepo := &mockAPIKeyRepository{}
	principal := &Principal{
		KeyID:     keyID,
		ProfileID: &profileID,
		Role:      service.APIKeyRoleMember,
	}

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request().WithContext(SetPrincipalForTest(c.Request().Context(), principal))
			c.SetRequest(req)
			return next(c)
		}
	})
	e.Use(LastUsedMiddleware(mockRepo))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	require.Eventually(t, func() bool {
		mockRepo.touchLastUsedMu.Lock()
		defer mockRepo.touchLastUsedMu.Unlock()
		return mockRepo.touchLastUsedCalled && mockRepo.touchLastUsedID == keyID
	}, time.Second, 10*time.Millisecond)
}

func TestLastUsedMiddleware_SkipsWhenRateLimited(t *testing.T) {
	e := newTestEcho()
	profileID := uuid.New()
	keyID := uuid.New()
	mockRepo := &mockAPIKeyRepository{}
	limiter := &stubRateLimitService{
		allowed:   false,
		remaining: 0,
		resetAt:   time.Now().Add(time.Minute),
	}
	cfg := &testRateLimitConfig{
		rateLimitPerMinute:      1,
		fragmentCreateRateLimit: 1,
		fragmentReadRateLimit:   1,
	}
	principal := &Principal{
		KeyID:     keyID,
		ProfileID: &profileID,
		Role:      service.APIKeyRoleMember,
	}

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request().WithContext(SetPrincipalForTest(c.Request().Context(), principal))
			c.SetRequest(req)
			return next(c)
		}
	})
	e.Use(RateLimitMiddleware(limiter, cfg, nil))
	e.Use(LastUsedMiddleware(mockRepo))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	e.GET("/test", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	mockRepo.touchLastUsedMu.Lock()
	assert.False(t, mockRepo.touchLastUsedCalled, "rate-limited requests should not touch last used")
	mockRepo.touchLastUsedMu.Unlock()
}

func TestPrincipalInterfaceAndRequireAuthHelpers(t *testing.T) {
	profileID := uuid.New()
	keyID := uuid.New()
	principal := &Principal{
		KeyID:       keyID,
		ProfileID:   &profileID,
		ProfileName: "Primary",
		Role:        service.APIKeyRoleManager,
		Scopes:      []string{"read", "write"},
		KeyPrefix:   "dm_test",
		RateLimit:   42,
	}

	if got := principal.GetKeyID(); got != keyID {
		t.Fatalf("GetKeyID() = %s, want %s", got, keyID)
	}
	if got := principal.GetTeamID(); got != profileID {
		t.Fatalf("GetTeamID() fallback = %s, want %s", got, profileID)
	}
	if got := principal.GetProfileID(); got == nil || *got != profileID {
		t.Fatalf("GetProfileID() = %v, want %s", got, profileID)
	}
	assert.Equal(t, "Primary", principal.GetProfileName())
	assert.Equal(t, service.APIKeyRoleManager, principal.GetRole())
	assert.Equal(t, []string{"read", "write"}, principal.GetScopes())
	assert.Equal(t, "dm_test", principal.GetKeyPrefix())
	assert.Equal(t, 42, principal.GetRateLimit())

	ctx := SetPrincipalForTest(context.Background(), principal)
	require.Same(t, principal, GetPrincipal(ctx))
	require.Same(t, principal, GetPrincipalInterface(ctx))

	required, err := RequirePrincipal(ctx)
	require.NoError(t, err)
	require.Same(t, principal, required)

	_, err = RequirePrincipal(context.Background())
	require.Error(t, err)

	e := newTestEcho()
	e.Use(RequireAuth())
	e.GET("/protected", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest(http.MethodGet, "/protected", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestVerifyKeyWithLimitCanceledContext(t *testing.T) {
	SetAuthVerificationConcurrency(1)
	t.Cleanup(func() { SetAuthVerificationConcurrency(0) })

	authVerifyLimiter.mu.RLock()
	slots := authVerifyLimiter.slots
	authVerifyLimiter.mu.RUnlock()
	require.NotNil(t, slots)
	slots <- struct{}{}
	defer func() { <-slots }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.False(t, verifyKeyWithLimit(ctx, "raw-key", "hash"))
}
