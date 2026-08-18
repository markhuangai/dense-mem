package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

type stubCredentialVerifier struct {
	valid bool
	err   error
}

func (s stubCredentialVerifier) Verify(context.Context, string, string) (bool, error) {
	return s.valid, s.err
}

func TestAuthMiddlewareRejectsCredentialVerificationAndEntitlementFailures(t *testing.T) {
	rawKey := "testprefix12345678901234567890"
	teamID := uuid.New()
	credentialID := uuid.New()
	baseCredential := testCredential(credentialID, teamID)
	baseCredential.Name = "Canonical credential"
	baseCredential.KeyHash = "encoded-hash"
	baseCredential.KeyPrefix = crypto.GetKeyPrefix(rawKey)
	baseCredential.Scopes = []string{"read"}
	baseCredential.Role = service.CredentialRoleMember

	tests := []struct {
		name      string
		verifier  stubCredentialVerifier
		validator SSOEntitlementValidator
		want      int
	}{
		{
			name:     "verifier unavailable",
			verifier: stubCredentialVerifier{err: context.DeadlineExceeded},
			want:     http.StatusServiceUnavailable,
		},
		{
			name:     "credential material does not verify",
			verifier: stubCredentialVerifier{},
			want:     http.StatusUnauthorized,
		},
		{
			name:     "entitlement no longer resolves a credential",
			verifier: stubCredentialVerifier{valid: true},
			validator: mockSSOEntitlementValidator{validateFunc: func(context.Context, *domain.Credential) (*domain.Credential, error) {
				return nil, nil
			}},
			want: http.StatusForbidden,
		},
		{
			name:     "entitlement returns an unbound credential",
			verifier: stubCredentialVerifier{valid: true},
			validator: mockSSOEntitlementValidator{validateFunc: func(_ context.Context, credential *domain.Credential) (*domain.Credential, error) {
				validated := *credential
				validated.TeamID = uuid.Nil
				return &validated, nil
			}},
			want: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEcho()
			repo := &mockCredentialRepository{getActiveByPrefixFunc: func(context.Context, string) (*domain.Credential, error) {
				credential := *baseCredential
				return &credential, nil
			}}
			e.Use(AuthMiddlewareWithOptions(repo, nil, nil, AuthOptions{
				CredentialVerifier:      tt.verifier,
				SSOEntitlementValidator: tt.validator,
			}))
			handlerCalled := false
			e.GET("/test", func(c echo.Context) error {
				handlerCalled = true
				return c.NoContent(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", "Bearer "+rawKey)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.False(t, handlerCalled)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestAuthMiddleware_SSOSessionFailureIsAuditedAndRecorded(t *testing.T) {
	e := newTestEcho()
	mockAudit := &mockAuditService{}
	mockSecurity := &mockSecurityService{}
	authenticator := mockSSOSessionAuthenticator{
		authenticateFunc: func(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error) {
			return nil, service.ErrSSOAccessDenied
		},
	}
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, mockAudit, mockSecurity, AuthOptions{
		SSOSessionAuthenticator: authenticator,
		AllowMissingCredentials: true,
	}))

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	req.RemoteAddr = "198.51.100.10:12345"
	req.AddCookie(&http.Cookie{Name: service.SSOSessionCookieName, Value: "session-token"})
	rec := httptest.NewRecorder()
	handlerCalled := false
	e.GET("/ui/api/session", func(c echo.Context) error {
		handlerCalled = true
		return c.String(http.StatusOK, "ok")
	})

	e.ServeHTTP(rec, req)

	assert.False(t, handlerCalled)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.True(t, mockAudit.authFailureCalled)
	assert.True(t, mockSecurity.recordAuthFailureCalled)
	assert.Equal(t, "SSO_AUTH_INVALID", mockSecurity.recordAuthFailureReason)
	assert.Equal(t, "api", mockSecurity.recordAuthFailureSurface)
	assert.Equal(t, "198.51.100.10", mockSecurity.recordAuthFailureIP)
}

func TestAuthMiddleware_OptionalMissingCredentialsHasNoFailureSideEffects(t *testing.T) {
	e := newTestEcho()
	mockAudit := &mockAuditService{}
	mockSecurity := &mockSecurityService{}
	handlerCalled := false

	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, mockAudit, mockSecurity, AuthOptions{
		AllowMissingCredentials: true,
	}))
	e.GET("/ui/api/session", func(c echo.Context) error {
		handlerCalled = true
		return c.NoContent(http.StatusUnauthorized)
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.True(t, handlerCalled)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, mockAudit.authFailureCalled)
	assert.False(t, mockSecurity.recordAuthFailureCalled)
}

func TestAuthMiddleware_MalformedScopedTeamRecordsAuthFailure(t *testing.T) {
	e := newTestEcho()
	mockAudit := &mockAuditService{}
	mockSecurity := &mockSecurityService{}
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, mockAudit, mockSecurity, AuthOptions{}))
	e.GET("/teams/:teamId/mcp", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/teams/not-a-uuid/mcp", nil)
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "INVALID_UUID")
	assert.True(t, mockAudit.authFailureCalled)
	assert.True(t, mockSecurity.recordAuthFailureCalled)
	assert.Equal(t, "TEAM_PATH_INVALID", mockSecurity.recordAuthFailureReason)
}
