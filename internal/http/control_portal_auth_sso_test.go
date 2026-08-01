package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func TestControlPortalMiddlewareUsesConfiguredControlOrigin(t *testing.T) {
	t.Parallel()

	identity := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{config: service.SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test"}},
	})
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(controlPortalMiddleware("secret", nil, identity))
	e.GET("/session", func(c echo.Context) error {
		return c.String(nethttp.StatusOK, controlPortalActorFromContext(c.Request().Context()))
	})

	request := httptest.NewRequest(nethttp.MethodOptions, "/session", nil)
	request.Header.Set(echo.HeaderOrigin, "https://control.example.test")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNoContent, response.Code)
	require.Equal(t, "https://control.example.test", response.Header().Get(echo.HeaderAccessControlAllowOrigin))
	require.Contains(t, response.Header().Get(echo.HeaderAccessControlAllowHeaders), service.ControlCSRFHeaderName)

	request = httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	request.Header.Set(echo.HeaderOrigin, "https://control.example.test")
	request.Header.Set("X-Control-Portal-Token", "secret")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "control_portal:x-control-portal-token", response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer secret")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "control_portal:authorization-bearer", response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	request.Header.Set(echo.HeaderOrigin, "https://untrusted.example.test")
	request.Header.Set("X-Control-Portal-Token", "secret")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusForbidden, response.Code)
}

func TestControlPortalAllowedOriginFailsClosed(t *testing.T) {
	t.Parallel()

	identity := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{err: errors.New("config unavailable")},
	})
	require.Empty(t, controlPortalAllowedOrigin(context.Background(), identity))
	require.Empty(t, controlPortalAllowedOrigin(context.Background(), nil))
	require.Equal(t, "control_portal", controlPortalActorFromContext(context.Background()))
}

func TestControlPortalMiddlewareBootstrapsOnlySameOriginAndNormalizesDefaultPorts(t *testing.T) {
	t.Parallel()

	bootstrap := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{config: service.SSORuntimeConfig{}},
	})
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(controlPortalMiddleware("secret", nil, bootstrap))
	e.POST("/config", func(c echo.Context) error { return c.NoContent(nethttp.StatusNoContent) })

	request := httptest.NewRequest(nethttp.MethodPost, "http://control.example.test/config", nil)
	request.Header.Set(echo.HeaderOrigin, "http://control.example.test")
	request.Header.Set("X-Control-Portal-Token", "secret")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNoContent, response.Code)
	require.Equal(t, "http://control.example.test", response.Header().Get(echo.HeaderAccessControlAllowOrigin))

	request = httptest.NewRequest(nethttp.MethodPost, "http://control.example.test/config", nil)
	request.Header.Set(echo.HeaderOrigin, "https://untrusted.example.test")
	request.Header.Set("X-Control-Portal-Token", "secret")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusForbidden, response.Code)

	configured := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{config: service.SSORuntimeConfig{ControlPublicBaseURL: "https://control.example.test:443"}},
	})
	e = echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(controlPortalMiddleware("secret", nil, configured))
	e.GET("/session", func(c echo.Context) error { return c.NoContent(nethttp.StatusNoContent) })
	request = httptest.NewRequest(nethttp.MethodGet, "https://control.example.test/session", nil)
	request.Header.Set(echo.HeaderOrigin, "https://control.example.test")
	request.Header.Set("X-Control-Portal-Token", "secret")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusNoContent, response.Code)
}

func TestControlPortalMiddlewareAcceptsCurrentSSOSessionAndRecordsSafeFailures(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	providerID := uuid.New()
	identityID := uuid.New()
	sessionToken := "control-sso-session"
	repo := newControlIdentityHTTPRepository()
	repo.groups = []*domain.ControlAdminGroup{{ProviderID: providerID, GroupID: "entra-control-admins", Enabled: true}}
	repo.sessions[service.HashSSOToken(sessionToken)] = &domain.ControlSession{
		SessionHash: service.HashSSOToken(sessionToken),
		IdentityID:  identityID,
		ProviderID:  providerID,
		GroupIDs:    []string{"entra-control-admins"},
		ExpiresAt:   now.Add(time.Hour),
	}
	ssoRepo := &controlIdentityHTTPSSORepository{
		providers:  map[uuid.UUID]*domain.SSOProvider{providerID: {ID: providerID, Enabled: true}},
		identities: map[uuid.UUID]*domain.SSOIdentity{identityID: {ID: identityID, ProviderID: providerID, Active: true}},
	}
	identity := service.NewControlIdentityService(repo, ssoRepo, service.ControlIdentityConfig{Now: func() time.Time { return now }})
	recorder := &controlAuthFailureRecorder{}
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(controlPortalMiddleware("static-token", recorder, identity))
	e.GET("/session", func(c echo.Context) error {
		return c.String(nethttp.StatusOK, controlPortalActorFromContext(c.Request().Context()))
	})

	request := httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	request.AddCookie(&nethttp.Cookie{Name: service.ControlSessionCookieName, Value: sessionToken})
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "control_portal:sso", response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusUnauthorized, response.Code)
	require.Equal(t, 1, recorder.calls)

	require.Equal(t, "control_portal", controlPortalActorFromRequest(httptest.NewRequest(nethttp.MethodGet, "/", nil)))
	invalidOrigin := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{config: service.SSORuntimeConfig{ControlPublicBaseURL: "://invalid"}},
	})
	require.Empty(t, controlPortalAllowedOrigin(context.Background(), invalidOrigin))
	require.Nil(t, controlDirectoryServiceError(nil))
	require.Nil(t, controlIdentityHTTPError(nil))
	directoryErr := controlDirectoryServiceError(errors.New("directory constraint failed: duplicate key value violates unique constraint \"credential_secret\""))
	require.Equal(t, "VALIDATION_ERROR: invalid directory connector request", directoryErr.Error())
	identityErr := controlIdentityHTTPError(errors.New("control admin constraint failed: duplicate key value violates unique constraint \"admin_group\""))
	require.Equal(t, "VALIDATION_ERROR: invalid control identity request", identityErr.Error())
}

type controlAuthFailureRecorder struct {
	service.SecurityService
	calls int
}

func (r *controlAuthFailureRecorder) RecordAuthFailure(context.Context, string, string, string) (*domain.SecurityIPBan, error) {
	r.calls++
	return nil, errors.New("security backend unavailable")
}
