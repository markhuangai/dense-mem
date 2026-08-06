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
	key *domain.APIKey
	err error
}

func (s userPortalSessionAuthStub) AuthenticateSession(context.Context, string, string, bool) (*domain.APIKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.key, nil
}

func TestAuthMiddlewareAuthenticatesPortalSessionAndSetsAuthMethod(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(AuthMiddlewareWithOptions(&mockAPIKeyRepository{}, nil, nil, AuthOptions{
		UserPortalSessionAuthenticator: userPortalSessionAuthStub{key: &domain.APIKey{
			ID:        keyID,
			TeamID:    teamID,
			Name:      "portal",
			Role:      service.APIKeyRoleMember,
			Scopes:    []string{"read"},
			RateLimit: 120,
		}},
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
	require.Contains(t, rec.Body.String(), `"auth_method":"api_key_session"`)
}

func TestAuthMiddlewareFailsClosedForInvalidPortalSession(t *testing.T) {
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(AuthMiddlewareWithOptions(&mockAPIKeyRepository{}, nil, nil, AuthOptions{
		UserPortalSessionAuthenticator: userPortalSessionAuthStub{err: service.ErrUserPortalSessionInvalid},
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
	e.Use(AuthMiddlewareWithOptions(&mockAPIKeyRepository{}, nil, nil, AuthOptions{
		UserPortalSessionAuthenticator: userPortalSessionAuthStub{err: service.ErrUserPortalCSRFInvalid},
	}))
	e.POST("/ui/api/key/rotate", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/ui/api/key/rotate", nil)
	req.AddCookie(&http.Cookie{Name: service.UserPortalSessionCookieName, Value: "opaque-session"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"FORBIDDEN"`)
}

var _ repository.APIKeyRepository = (*mockAPIKeyRepository)(nil)
