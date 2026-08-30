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

func TestControlPortalMiddlewareIgnoresOriginBehindTLSProxy(t *testing.T) {
	t.Parallel()

	identity := service.NewControlIdentityService(nil, nil, service.ControlIdentityConfig{
		RuntimeConfig: controlIdentityHTTPRuntime{config: service.SSORuntimeConfig{}},
	})
	e := echo.New()
	e.HTTPErrorHandler = httperr.ErrorHandler
	e.Use(controlPortalMiddleware("secret", nil, identity))
	e.GET("/session", func(c echo.Context) error {
		return c.String(nethttp.StatusOK, controlPortalActorFromContext(c.Request().Context()))
	})

	request := httptest.NewRequest(nethttp.MethodGet, "http://dense-mem:8090/session", nil)
	request.Header.Set(echo.HeaderOrigin, "https://control.example.test")
	request.Header.Set("X-Control-Portal-Token", "secret")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "control_portal:x-control-portal-token", response.Body.String())
	require.Empty(t, response.Header().Get(echo.HeaderAccessControlAllowOrigin))
	require.Empty(t, response.Header().Get(echo.HeaderAccessControlAllowCredentials))

	request = httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	request.Header.Set(echo.HeaderAuthorization, "Bearer secret")
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "control_portal:authorization-bearer", response.Body.String())
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
	e.GET("/identity", func(c echo.Context) error {
		return c.String(nethttp.StatusOK, controlPortalActorIdentityFromContext(c.Request().Context()))
	})

	request := httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	request.AddCookie(&nethttp.Cookie{Name: service.ControlSessionCookieName, Value: sessionToken})
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "control_portal:sso", response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/identity", nil)
	request.AddCookie(&nethttp.Cookie{Name: service.ControlSessionCookieName, Value: sessionToken})
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusOK, response.Code, response.Body.String())
	require.Equal(t, identityID.String(), response.Body.String())

	request = httptest.NewRequest(nethttp.MethodGet, "/session", nil)
	response = httptest.NewRecorder()
	e.ServeHTTP(response, request)
	require.Equal(t, nethttp.StatusUnauthorized, response.Code)
	require.Equal(t, 1, recorder.calls)

	require.Equal(t, "control_portal", controlPortalActorFromContext(context.Background()))
	require.Equal(t, "control_portal", controlPortalActorFromRequest(httptest.NewRequest(nethttp.MethodGet, "/", nil)))
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
