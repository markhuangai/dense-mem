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

// mockCredentialRepository is a mock implementation of repository.CredentialRepository
type mockCredentialRepository struct {
	getActiveByPrefixFunc func(ctx context.Context, prefix string) (*domain.Credential, error)
	touchLastUsedFunc     func(ctx context.Context, id uuid.UUID) error
	touchLastUsedCalled   bool
	touchLastUsedID       uuid.UUID
	touchLastUsedMu       sync.Mutex
}

func (m *mockCredentialRepository) CreateCredential(ctx context.Context, key *domain.Credential) error {
	return nil
}

func (m *mockCredentialRepository) ListByTeam(ctx context.Context, profileID uuid.UUID, limit, offset int) ([]*domain.Credential, error) {
	return nil, nil
}

func (m *mockCredentialRepository) GetActiveByPrefix(ctx context.Context, prefix string) (*domain.Credential, error) {
	if m.getActiveByPrefixFunc != nil {
		return m.getActiveByPrefixFunc(ctx, prefix)
	}
	return nil, nil
}

func (m *mockCredentialRepository) RevokeForTeam(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	return 0, nil
}

func (m *mockCredentialRepository) RotateForTeam(ctx context.Context, profileID, id uuid.UUID, keyHash, keyPrefix, keySuffix string, expiresAt *time.Time) (int64, error) {
	return 0, nil
}

func (m *mockCredentialRepository) UpdateNameForTeam(ctx context.Context, profileID, id uuid.UUID, name string) (int64, error) {
	return 0, nil
}

func (m *mockCredentialRepository) UpdateRoleForTeam(ctx context.Context, profileID, id uuid.UUID, role string, scopes []string) (int64, error) {
	return 0, nil
}

func (m *mockCredentialRepository) UpdateScopesForTeam(ctx context.Context, profileID, id uuid.UUID, scopes []string) (int64, error) {
	return 0, nil
}

func (m *mockCredentialRepository) DeleteForTeam(ctx context.Context, profileID, id uuid.UUID) (int64, error) {
	return 0, nil
}

func (m *mockCredentialRepository) GetByIDForTeam(ctx context.Context, profileID, id uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}

func (m *mockCredentialRepository) ListSSOOwnedCredentials(ctx context.Context, profileID, identityID uuid.UUID) ([]*domain.Credential, error) {
	return nil, nil
}

func (m *mockCredentialRepository) GetSSOOwnedCredentialByID(ctx context.Context, profileID, identityID, credentialID uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}

func (m *mockCredentialRepository) GetSSOOwnedCredential(ctx context.Context, profileID, identityID uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}

func (m *mockCredentialRepository) CountByTeam(ctx context.Context, profileID uuid.UUID) (int64, error) {
	return 0, nil
}

func (m *mockCredentialRepository) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	m.touchLastUsedMu.Lock()
	m.touchLastUsedCalled = true
	m.touchLastUsedID = id
	m.touchLastUsedMu.Unlock()
	if m.touchLastUsedFunc != nil {
		return m.touchLastUsedFunc(ctx, id)
	}
	return nil
}

func (m *mockCredentialRepository) RecordLastUsed(id uuid.UUID, _ time.Time) {
	_ = m.TouchLastUsed(context.Background(), id)
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

func (m *mockAuditService) TeamCreated(ctx context.Context, profileID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) TeamUpdated(ctx context.Context, profileID string, beforePayload, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) TeamDeleteBlocked(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string, reason string) error {
	return nil
}

func (m *mockAuditService) TeamDeleted(ctx context.Context, profileID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) CredentialCreated(ctx context.Context, profileID *string, keyID string, afterPayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
	return nil
}

func (m *mockAuditService) CredentialRevoked(ctx context.Context, profileID *string, keyID string, beforePayload map[string]interface{}, actorKeyID *string, actorRole, clientIP, correlationID string) error {
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

func (m *mockAuditService) CrossTeamDenied(ctx context.Context, actorProfileID, targetProfileID string, operation string, metadata map[string]interface{}, clientIP, correlationID string) error {
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
	validateFunc func(ctx context.Context, key *domain.Credential) (*domain.Credential, error)
}

func (m mockSSOEntitlementValidator) ValidateCredential(ctx context.Context, key *domain.Credential) (*domain.Credential, error) {
	return m.validateFunc(ctx, key)
}

type mockSSOSessionAuthenticator struct {
	authenticateFunc func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error)
}

func (m mockSSOSessionAuthenticator) AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error) {
	return m.authenticateFunc(ctx, sessionToken, csrfToken, requireCSRF)
}

func testCredential(id, teamID uuid.UUID) *domain.Credential {
	return &domain.Credential{
		ID:              id,
		ActorIdentityID: id,
		MembershipID:    id,
		OwnerID:         id,
		TeamID:          teamID,
	}
}

func testUUIDPtr(id uuid.UUID) *uuid.UUID {
	return &id
}

func testSSOActor(teamID, identityID, membershipID, ownerID uuid.UUID, providerID *uuid.UUID, grants []string) *domain.AuthenticatedActor {
	return &domain.AuthenticatedActor{
		Team:     domain.Team{ID: teamID, Name: "SSO team"},
		Identity: domain.ActorIdentity{ID: identityID, Kind: "human", DisplayName: "SSO member"},
		Membership: domain.Membership{
			ID:              membershipID,
			ActorIdentityID: identityID,
			TeamID:          teamID,
			OwnerID:         ownerID,
			Name:            "SSO member",
			Grants:          append([]string(nil), grants...),
			Role:            service.CredentialRoleMember,
			Status:          "active",
			SSOProviderID:   providerID,
		},
		OwnerID: ownerID,
	}
}

// newTestEcho creates a new Echo instance with the custom error handler
func newTestEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	return e
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	e := newTestEcho()
	mockRepo := &mockCredentialRepository{}
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
	mockRepo := &mockCredentialRepository{}
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
	mockRepo := &mockCredentialRepository{}
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
	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
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

	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			return &domain.Credential{
				ID:        keyID,
				TeamID:    profileID,
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

	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			return &domain.Credential{
				ID:        keyID,
				TeamID:    profileID,
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

	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			return &domain.Credential{
				ID: keyID, ActorIdentityID: keyID, MembershipID: keyID, OwnerID: keyID,
				TeamID: profileID, KeyHash: keyHash, Scopes: []string{"read", "write"},
				Role: service.CredentialRoleManager,
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
	require.NotNil(t, capturedPrincipal.CredentialID)
	assert.Equal(t, keyID, *capturedPrincipal.CredentialID)
	assert.Equal(t, profileID, capturedPrincipal.TeamID)
	assert.Equal(t, keyID, capturedPrincipal.IdentityID)
	assert.Equal(t, keyID, capturedPrincipal.MembershipID)
	assert.Equal(t, keyID, capturedPrincipal.OwnerID)
	assert.Equal(t, service.CredentialRoleManager, capturedPrincipal.Role)
	assert.Equal(t, []string{"read", "write"}, capturedPrincipal.Grants)
	assert.Equal(t, rawKey[:24], capturedPrincipal.KeyPrefix)
}

func TestAuthMiddleware_SSOEntitlementValidatorOverridesPrincipal(t *testing.T) {
	e := newTestEcho()
	originalTeamID := uuid.New()
	originalProfileID := uuid.New()
	providerID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			return &domain.Credential{
				ID: originalProfileID, ActorIdentityID: originalProfileID,
				MembershipID: originalProfileID, OwnerID: originalProfileID,
				TeamID: originalTeamID, Name: "Original credential", TeamName: "Original team",
				KeyHash: keyHash, Scopes: []string{"read"}, Role: service.CredentialRoleMember, RateLimit: 10,
			}, nil
		},
	}
	validator := mockSSOEntitlementValidator{
		validateFunc: func(ctx context.Context, key *domain.Credential) (*domain.Credential, error) {
			require.Equal(t, originalProfileID, key.ID)
			validated := *key
			validated.Scopes = []string{"read", "write"}
			validated.Role = service.CredentialRoleManager
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
	var capturedActor requestctx.Actor
	var actorOK bool
	e.GET("/test", func(c echo.Context) error {
		capturedPrincipal = GetPrincipal(c.Request().Context())
		capturedActor, actorOK = requestctx.ActorFromContext(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, capturedPrincipal)
	require.NotNil(t, capturedPrincipal.CredentialID)
	assert.Equal(t, originalProfileID, *capturedPrincipal.CredentialID)
	assert.Equal(t, originalTeamID, capturedPrincipal.TeamID)
	assert.Equal(t, originalProfileID, capturedPrincipal.OwnerID)
	assert.Equal(t, "Original credential", capturedPrincipal.OwnerName)
	assert.Equal(t, service.CredentialRoleManager, capturedPrincipal.Role)
	assert.Equal(t, []string{"read", "write"}, capturedPrincipal.Grants)
	assert.Equal(t, 77, capturedPrincipal.RateLimit)
	require.NotNil(t, capturedPrincipal.SSOProviderID)
	assert.Equal(t, providerID, *capturedPrincipal.SSOProviderID)
	assert.Equal(t, "subject-123", capturedPrincipal.SSOSubject)
	require.True(t, actorOK)
	assert.Equal(t, originalTeamID, capturedActor.TeamID)
	assert.Equal(t, "Original team", capturedActor.TeamName)
	assert.Equal(t, originalProfileID, capturedActor.OwnerID)
	assert.Equal(t, "Original credential", capturedActor.OwnerName)
}

func TestAuthMiddleware_SSOEntitlementValidatorDeniesKey(t *testing.T) {
	e := newTestEcho()
	teamID := uuid.New()
	keyID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)

	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			return &domain.Credential{
				ID:      keyID,
				TeamID:  teamID,
				KeyHash: keyHash,
				Scopes:  []string{"read"},
				Role:    service.CredentialRoleMember,
			}, nil
		},
	}
	mockAudit := &mockAuditService{}
	validator := mockSSOEntitlementValidator{
		validateFunc: func(ctx context.Context, key *domain.Credential) (*domain.Credential, error) {
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
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error) {
			require.Equal(t, "session-token", sessionToken)
			require.Empty(t, csrfToken)
			require.False(t, requireCSRF)
			return testSSOActor(teamID, uuid.New(), profileID, profileID, &providerID, []string{"read"}), nil
		},
	}

	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{SSOSessionAuthenticator: authenticator}))

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
	assert.Nil(t, capturedPrincipal.CredentialID)
	assert.Equal(t, profileID, capturedPrincipal.OwnerID)
	assert.Equal(t, teamID, capturedPrincipal.TeamID)
	assert.Equal(t, []string{"read"}, capturedPrincipal.Grants)
	require.NotNil(t, capturedPrincipal.SSOProviderID)
	assert.Equal(t, providerID, *capturedPrincipal.SSOProviderID)
}

func TestAuthMiddleware_SSOSessionRequiresCSRFHeaderForUnsafeMethods(t *testing.T) {
	e := newTestEcho()
	authenticator := mockSSOSessionAuthenticator{
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error) {
			require.Equal(t, "session-token", sessionToken)
			require.Empty(t, csrfToken)
			require.True(t, requireCSRF)
			return nil, service.ErrSSOCSRFInvalid
		},
	}
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{SSOSessionAuthenticator: authenticator}))

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
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error) {
			require.Equal(t, "session-token", sessionToken)
			require.Equal(t, "header-csrf", csrfToken)
			require.True(t, requireCSRF)
			return testSSOActor(teamID, uuid.New(), profileID, profileID, nil, []string{"read"}), nil
		},
	}
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{SSOSessionAuthenticator: authenticator}))

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
	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			prefixes = append(prefixes, prefix)
			if prefix != rawKey[:12] {
				return nil, nil
			}
			return &domain.Credential{
				ID: keyID, ActorIdentityID: keyID, MembershipID: keyID, OwnerID: keyID,
				TeamID: profileID, KeyHash: keyHash, Scopes: []string{"read"},
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

	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			return &domain.Credential{
				ID:        keyID,
				TeamID:    uuid.Nil,
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

	mockRepo := &mockCredentialRepository{
		getActiveByPrefixFunc: func(ctx context.Context, prefix string) (*domain.Credential, error) {
			return &domain.Credential{
				ID: keyID, ActorIdentityID: keyID, MembershipID: keyID, OwnerID: keyID,
				TeamID: profileID, KeyHash: keyHash, Scopes: []string{"read"},
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
	mockRepo := &mockCredentialRepository{}
	principal := &Principal{
		TeamID:       uuid.New(),
		OwnerID:      profileID,
		CredentialID: &keyID,
		Role:         service.CredentialRoleMember,
		AuthMethod:   "api_key",
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
	mockRepo := &mockCredentialRepository{}
	limiter := &stubRateLimitService{
		allowed:   false,
		remaining: 0,
		resetAt:   time.Now().Add(time.Minute),
	}
	cfg := &testRateLimitConfig{rateLimitPerMinute: 1}
	principal := &Principal{
		TeamID:       uuid.New(),
		OwnerID:      profileID,
		CredentialID: &keyID,
		Role:         service.CredentialRoleMember,
		AuthMethod:   "api_key",
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
		TeamID:       profileID,
		IdentityID:   keyID,
		MembershipID: uuid.New(),
		OwnerID:      keyID,
		OwnerName:    "Primary",
		CredentialID: &keyID,
		Role:         service.CredentialRoleManager,
		Grants:       []string{"read", "write"},
		KeyPrefix:    "dm_test",
		RateLimit:    42,
	}

	if got := principal.GetCredentialID(); got == nil || *got != keyID {
		t.Fatalf("GetCredentialID() = %v, want %s", got, keyID)
	}
	if got := principal.GetTeamID(); got != profileID {
		t.Fatalf("GetTeamID() = %s, want %s", got, profileID)
	}
	assert.Equal(t, keyID, principal.GetIdentityID())
	assert.Equal(t, principal.MembershipID, principal.GetMembershipID())
	assert.Equal(t, keyID, principal.GetOwnerID())
	assert.Equal(t, "Primary", principal.GetOwnerName())
	assert.Equal(t, service.CredentialRoleManager, principal.GetRole())
	assert.Equal(t, []string{"read", "write"}, principal.GetGrants())
	assert.Equal(t, "dm_test", principal.GetKeyPrefix())
	assert.Equal(t, 42, principal.GetRateLimit())
	assert.Nil(t, authenticatedActorFromCredential(nil))

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

func TestCredentialVerifierCanceledContext(t *testing.T) {
	verifier := crypto.NewArgon2Verifier(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	valid, err := verifier.Verify(ctx, "raw-key", "hash")
	assert.False(t, valid)
	assert.ErrorIs(t, err, context.Canceled)
}
