package middleware

import (
	"context"
	"errors"
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

type stubOAuthBearerAuthenticator struct {
	actor      *domain.AuthenticatedActor
	err        error
	rawToken   string
	pathTeamID *uuid.UUID
	calls      int
}

func (s *stubOAuthBearerAuthenticator) AuthenticateOAuthBearer(_ context.Context, rawToken string, pathTeamID *uuid.UUID) (*domain.AuthenticatedActor, error) {
	s.calls++
	s.rawToken = rawToken
	if pathTeamID != nil {
		teamID := *pathTeamID
		s.pathTeamID = &teamID
	}
	return s.actor, s.err
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

func TestAuthMiddlewareOAuthBearerSetsImmutablePrincipal(t *testing.T) {
	teamID := uuid.New()
	identityID := uuid.New()
	membershipID := uuid.New()
	ownerID := uuid.New()
	providerID := uuid.New()
	actor := testSSOActor(teamID, identityID, membershipID, ownerID, &providerID, []string{"read"})
	actor.Membership.MemorySpaceID = uuid.New()
	authenticator := &stubOAuthBearerAuthenticator{actor: actor}
	e := newTestEcho()
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{
		OAuthBearerAuthenticator: authenticator,
	}))
	e.GET("/teams/:teamId/mcp", func(c echo.Context) error {
		principal := GetPrincipal(c.Request().Context())
		assert.NotNil(t, principal)
		assert.Equal(t, teamID, principal.TeamID)
		assert.Equal(t, identityID, principal.IdentityID)
		assert.Equal(t, membershipID, principal.MembershipID)
		assert.Equal(t, ownerID, principal.OwnerID)
		assert.Equal(t, "oauth", principal.AuthMethod)
		assert.Nil(t, principal.CredentialID)
		assert.Equal(t, []string{"read"}, principal.Grants)
		assert.Equal(t, []domain.MemorySpaceAccess{
			{ID: uuid.Nil, Kind: domain.MemorySpaceTeamShared},
			{ID: actor.Membership.MemorySpaceID, Kind: domain.MemorySpaceProfilePrivate},
		}, principal.AllowedSpaces)
		assert.Empty(t, c.Request().Header.Get("Authorization"))
		return c.NoContent(http.StatusOK)
	})

	rawToken := "header.payload.signature"
	req := httptest.NewRequest(http.MethodGet, "/teams/"+teamID.String()+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, authenticator.calls)
	assert.Equal(t, rawToken, authenticator.rawToken)
	if assert.NotNil(t, authenticator.pathTeamID) {
		assert.Equal(t, teamID, *authenticator.pathTeamID)
	}
}

func TestAuthMiddlewareOAuthBearerMapsBoundedErrors(t *testing.T) {
	tests := []struct {
		name                string
		err                 error
		wantStatus          int
		wantCode            string
		wantSecurityFailure bool
	}{
		{name: "expired", err: service.ErrOAuthTokenExpired, wantStatus: http.StatusUnauthorized, wantCode: "AUTH_EXPIRED", wantSecurityFailure: true},
		{name: "invalid", err: service.ErrOAuthTokenInvalid, wantStatus: http.StatusUnauthorized, wantCode: "AUTH_INVALID", wantSecurityFailure: true},
		{name: "ambiguous team", err: service.ErrOAuthTeamRequired, wantStatus: http.StatusBadRequest, wantCode: "team_required", wantSecurityFailure: true},
		{name: "membership denied", err: service.ErrOAuthAccessDenied, wantStatus: http.StatusForbidden, wantCode: "FORBIDDEN", wantSecurityFailure: true},
		{name: "provider unavailable", err: service.ErrOAuthProviderUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "SERVICE_UNAVAILABLE"},
		{name: "unknown", err: errors.New("backend details"), wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL_ERROR", wantSecurityFailure: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			audit := &mockAuditService{}
			security := &mockSecurityService{}
			e := newTestEcho()
			e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, audit, security, AuthOptions{
				OAuthBearerAuthenticator: &stubOAuthBearerAuthenticator{err: test.err},
			}))
			e.GET("/mcp", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer header.payload.signature")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, test.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), `"code":"`+test.wantCode+`"`)
			assert.NotContains(t, rec.Body.String(), "backend details")
			assert.True(t, audit.authFailureCalled)
			assert.Equal(t, test.wantSecurityFailure, security.recordAuthFailureCalled)
		})
	}
}

func TestAuthMiddlewareStaticCredentialMustMatchScopedTeam(t *testing.T) {
	credentialTeamID := uuid.New()
	credentialID := uuid.New()
	rawKey := "testprefix12345678901234567890"
	credential := testCredential(credentialID, credentialTeamID)
	credential.Name = "Static MCP credential"
	credential.KeyHash = "encoded-hash"
	credential.KeyPrefix = crypto.GetKeyPrefix(rawKey)
	credential.Scopes = []string{"read"}
	credential.Role = service.CredentialRoleMember

	for _, test := range []struct {
		name       string
		pathTeamID uuid.UUID
		wantStatus int
	}{
		{name: "match", pathTeamID: credentialTeamID, wantStatus: http.StatusOK},
		{name: "mismatch", pathTeamID: uuid.New(), wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newTestEcho()
			e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{getActiveByPrefixFunc: func(context.Context, string) (*domain.Credential, error) {
				copy := *credential
				return &copy, nil
			}}, nil, nil, AuthOptions{CredentialVerifier: stubCredentialVerifier{valid: true}}))
			e.GET("/teams/:teamId/mcp", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/teams/"+test.pathTeamID.String()+"/mcp", nil)
			req.Header.Set("Authorization", "Bearer "+rawKey)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, test.wantStatus, rec.Code)
		})
	}
}

func TestAuthMiddlewareOAuthOnlyRejectsBrowserCookies(t *testing.T) {
	authenticator := &stubOAuthBearerAuthenticator{}
	e := newTestEcho()
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{
		OAuthBearerAuthenticator: authenticator,
	}))
	e.GET("/mcp", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.AddCookie(&http.Cookie{Name: service.SSOSessionCookieName, Value: "sso-session"})
	req.AddCookie(&http.Cookie{Name: service.UserPortalSessionCookieName, Value: "portal-session"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Zero(t, authenticator.calls)
}
