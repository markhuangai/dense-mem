package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type portalSessionManagerStub struct {
	createdKeyID uuid.UUID
	logoutToken  string
	result       *service.UserPortalSessionResult
}

func invokeCreatePortalSession(t *testing.T, h *userPortalHandler, c echo.Context) error {
	t.Helper()
	return httpmw.BindAndValidateStrict[dto.CreateUserPortalSessionRequest](userPortalCreateSessionBodyKey)(h.createPortalSession)(c)
}

func (s *portalSessionManagerStub) CreateSession(_ context.Context, keyID uuid.UUID) (*service.UserPortalSessionResult, error) {
	s.createdKeyID = keyID
	return s.result, nil
}

func (*portalSessionManagerStub) AuthenticateSession(context.Context, string, string, bool) (*domain.APIKey, error) {
	return nil, service.ErrUserPortalSessionInvalid
}

func (s *portalSessionManagerStub) Logout(_ context.Context, token string) error {
	s.logoutToken = token
	return nil
}

func TestCreatePortalSessionSetsHostOnlyUiCookies(t *testing.T) {
	keyID := uuid.New()
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	manager := &portalSessionManagerStub{result: &service.UserPortalSessionResult{
		SessionToken: "opaque-session",
		CSRFToken:    "csrf-token",
		ExpiresAt:    expires,
	}}
	h := &userPortalHandler{portal: manager}
	c := userPortalEchoContext(t, http.MethodPost, "/ui/api/session", `{"remember":true}`, &httpmw.Principal{
		KeyID:      keyID,
		TeamID:     uuid.New(),
		AuthMethod: "api_key",
	})

	err := invokeCreatePortalSession(t, h, c)
	require.NoError(t, err)
	require.Equal(t, keyID, manager.createdKeyID)

	recorder, ok := c.Response().Writer.(*httptest.ResponseRecorder)
	require.True(t, ok)
	cookies := recorder.Result().Cookies()
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case service.UserPortalSessionCookieName:
			sessionCookie = cookie
		case service.UserPortalCSRFCookieName:
			csrfCookie = cookie
		}
	}
	require.NotNil(t, sessionCookie)
	require.NotNil(t, csrfCookie)
	require.Equal(t, "/ui", sessionCookie.Path)
	require.True(t, sessionCookie.HttpOnly)
	require.False(t, csrfCookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, sessionCookie.SameSite)
	require.Equal(t, expires, sessionCookie.Expires)
	require.Greater(t, sessionCookie.MaxAge, 0)
	require.Empty(t, sessionCookie.Domain)
	require.False(t, sessionCookie.Secure)
}

func TestCreatePortalSessionUsesBrowserSessionCookiesWhenRememberDisabled(t *testing.T) {
	manager := &portalSessionManagerStub{result: &service.UserPortalSessionResult{
		SessionToken: "opaque-session",
		CSRFToken:    "csrf-token",
		ExpiresAt:    time.Now().UTC().Add(7 * 24 * time.Hour),
	}}
	h := &userPortalHandler{portal: manager}
	c := userPortalEchoContext(t, http.MethodPost, "/ui/api/session", `{"remember":false}`, &httpmw.Principal{
		KeyID:      uuid.New(),
		TeamID:     uuid.New(),
		AuthMethod: "api_key",
	})

	require.NoError(t, invokeCreatePortalSession(t, h, c))
	recorder, ok := c.Response().Writer.(*httptest.ResponseRecorder)
	require.True(t, ok)
	found := 0
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name != service.UserPortalSessionCookieName && cookie.Name != service.UserPortalCSRFCookieName {
			continue
		}
		found++
		require.Equal(t, "/ui", cookie.Path)
		require.True(t, cookie.Expires.IsZero())
		require.Equal(t, 0, cookie.MaxAge)
	}
	require.Equal(t, 2, found)
}

func TestCreatePortalSessionRejectsUnknownOrMissingFields(t *testing.T) {
	manager := &portalSessionManagerStub{result: &service.UserPortalSessionResult{SessionToken: "s", CSRFToken: "c", ExpiresAt: time.Now().Add(time.Hour)}}
	h := &userPortalHandler{portal: manager}
	principal := &httpmw.Principal{KeyID: uuid.New(), TeamID: uuid.New(), AuthMethod: "api_key"}

	for _, body := range []string{`{}`, `{"remember":true,"unexpected":true}`, `{"remember":true} {}`} {
		c := userPortalEchoContext(t, http.MethodPost, "/ui/api/session", body, principal)
		err := invokeCreatePortalSession(t, h, c)
		var apiErr *httperr.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, httperr.VALIDATION_ERROR, apiErr.Code)
	}
}

func TestCreatePortalSessionRequiresDirectAPIKeyAndService(t *testing.T) {
	manager := &portalSessionManagerStub{result: &service.UserPortalSessionResult{SessionToken: "s", CSRFToken: "c", ExpiresAt: time.Now().Add(time.Hour)}}
	for _, principal := range []*httpmw.Principal{
		nil,
		{KeyID: uuid.New(), TeamID: uuid.New(), AuthMethod: "api_key_session"},
	} {
		h := &userPortalHandler{portal: manager}
		err := invokeCreatePortalSession(t, h, userPortalEchoContext(t, http.MethodPost, "/ui/api/session", `{"remember":true}`, principal))
		var apiErr *httperr.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, httperr.FORBIDDEN, apiErr.Code)
	}

	h := &userPortalHandler{}
	err := invokeCreatePortalSession(t, h, userPortalEchoContext(t, http.MethodPost, "/ui/api/session", `{"remember":true}`, &httpmw.Principal{
		KeyID:      uuid.New(),
		TeamID:     uuid.New(),
		AuthMethod: "api_key",
	}))
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.SERVICE_UNAVAILABLE, apiErr.Code)
}

func TestUserPortalSessionErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code httperr.ErrorCode
	}{
		{name: "invalid session", err: service.ErrUserPortalSessionInvalid, code: httperr.AUTH_INVALID},
		{name: "invalid csrf", err: service.ErrUserPortalCSRFInvalid, code: httperr.FORBIDDEN},
		{name: "unknown", err: errors.New("unexpected"), code: httperr.INTERNAL_ERROR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var apiErr *httperr.APIError
			err := userPortalSessionError(tt.err)
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, tt.code, apiErr.Code)
		})
	}
	require.NoError(t, userPortalSessionError(nil))
}

func TestLogoutPortalSessionRequiresDoubleSubmitCsrfAndClearsCookies(t *testing.T) {
	manager := &portalSessionManagerStub{}
	h := &userPortalHandler{portal: manager}
	c := userPortalEchoContext(t, http.MethodPost, "/ui/api/session/logout", "", nil)
	cookieHeader := strings.Join([]string{
		service.UserPortalSessionCookieName + "=opaque-session",
		service.UserPortalCSRFCookieName + "=csrf-token",
	}, "; ")
	c.Request().Header.Set("Cookie", cookieHeader)
	c.Request().Header.Set(service.SSOCSRFHeaderName, "csrf-token")

	require.NoError(t, h.logoutPortalSession(c))
	require.Equal(t, "opaque-session", manager.logoutToken)
	cleared := map[string]*http.Cookie{}
	recorder, ok := c.Response().Writer.(*httptest.ResponseRecorder)
	require.True(t, ok)
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.MaxAge < 0 {
			cleared[cookie.Name] = cookie
		}
	}
	require.Equal(t, "/ui", cleared[service.UserPortalSessionCookieName].Path)
	require.Equal(t, "/ui", cleared[service.UserPortalCSRFCookieName].Path)

	invalid := userPortalEchoContext(t, http.MethodPost, "/ui/api/session/logout", "", nil)
	invalid.Request().Header.Set("Cookie", service.UserPortalSessionCookieName+"=opaque-session; "+service.UserPortalCSRFCookieName+"=csrf-token")
	invalid.Request().Header.Set(service.SSOCSRFHeaderName, "wrong")
	err := h.logoutPortalSession(invalid)
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.FORBIDDEN, apiErr.Code)
}
