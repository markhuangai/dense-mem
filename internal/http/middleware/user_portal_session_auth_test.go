package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

type userPortalSessionAuthStub struct {
	key             *domain.Credential
	err             error
	gotSessionToken string
	gotCSRFToken    string
	gotRequireCSRF  bool
}

func (s *userPortalSessionAuthStub) AuthenticateSession(_ context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error) {
	s.gotSessionToken = sessionToken
	s.gotCSRFToken = csrfToken
	s.gotRequireCSRF = requireCSRF
	if s.err != nil {
		return nil, s.err
	}
	return authenticatedActorFromCredential(s.key), nil
}

func TestAuthMiddlewareAuthenticatesPortalSessionAndSetsAuthMethod(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	stub := &userPortalSessionAuthStub{key: &domain.Credential{
		ID: keyID, ActorIdentityID: keyID, MembershipID: keyID, OwnerID: keyID,
		TeamID: teamID, Name: "portal", Role: service.CredentialRoleMember,
		Scopes: []string{"read"}, RateLimit: 120,
	}}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{
		UserPortalSessionAuthenticator: stub,
	}))
	e.GET("/ui/api/session", func(c echo.Context) error {
		principal := GetPrincipal(c.Request().Context())
		if principal == nil {
			return httperr.New(httperr.AUTH_MISSING, "missing principal")
		}
		return c.JSON(http.StatusOK, map[string]string{"auth_method": principal.AuthMethod})
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	req.AddCookie(&http.Cookie{Name: service.UserPortalSessionCookieName, Value: "opaque-session"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"auth_method":"credential_session"`)
	require.Equal(t, "opaque-session", stub.gotSessionToken)
	require.Empty(t, stub.gotCSRFToken)
	require.False(t, stub.gotRequireCSRF)
}

func TestAuthMiddlewareFailsClosedForInvalidPortalSession(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	stub := &userPortalSessionAuthStub{err: service.ErrUserPortalSessionInvalid}
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{
		UserPortalSessionAuthenticator: stub,
		AllowMissingCredentials:        true,
	}))
	e.GET("/ui/api/session", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	req.AddCookie(&http.Cookie{Name: service.UserPortalSessionCookieName, Value: "stale-session"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"AUTH_INVALID"`)
}

func TestAuthMiddlewareRequiresPortalCSRFForUnsafeRequests(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	stub := &userPortalSessionAuthStub{err: service.ErrUserPortalCSRFInvalid}
	e.Use(AuthMiddlewareWithOptions(&mockCredentialRepository{}, nil, nil, AuthOptions{
		UserPortalSessionAuthenticator: stub,
	}))
	e.POST("/ui/api/credential/rotate", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/ui/api/credential/rotate", nil)
	req.AddCookie(&http.Cookie{Name: service.UserPortalSessionCookieName, Value: "opaque-session"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"FORBIDDEN"`)
	require.Equal(t, "opaque-session", stub.gotSessionToken)
	require.Empty(t, stub.gotCSRFToken)
	require.True(t, stub.gotRequireCSRF)
}

var _ repository.CredentialRepository = (*mockCredentialRepository)(nil)
