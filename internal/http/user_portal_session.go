package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type userPortalCreateSessionRequest struct {
	Remember *bool `json:"remember"`
}

func (h *userPortalHandler) createPortalSession(c echo.Context) error {
	principal := httpmw.GetPrincipal(c.Request().Context())
	if principal == nil || principal.AuthMethod != "api_key" {
		return httperr.New(httperr.FORBIDDEN, "direct API key authentication required")
	}
	if h.portal == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "user portal session service unavailable")
	}

	var body userPortalCreateSessionRequest
	decoder := json.NewDecoder(c.Request().Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Remember == nil {
		return httperr.New(httperr.VALIDATION_ERROR, "session request must contain remember")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return httperr.New(httperr.VALIDATION_ERROR, "session request must contain one JSON object")
	}

	result, err := h.portal.CreateSession(c.Request().Context(), principal.KeyID)
	if err != nil {
		return userPortalSessionError(err)
	}
	secure := h.portalCookieSecure(c.Request().Context())
	setUserPortalCookie(c, service.UserPortalSessionCookieName, result.SessionToken, true, *body.Remember, result.ExpiresAt, secure)
	setUserPortalCookie(c, service.UserPortalCSRFCookieName, result.CSRFToken, false, *body.Remember, result.ExpiresAt, secure)
	clearSSOCookie(c, service.SSOSessionCookieName)
	clearSSOCookie(c, service.SSOCSRFCookieName)
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "signed_in"}})
}

func (h *userPortalHandler) logoutPortalSession(c echo.Context) error {
	if cookie, err := c.Request().Cookie(service.UserPortalSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		if err := validateUserPortalLogoutCSRF(c); err != nil {
			return err
		}
		if h.portal != nil {
			if err := h.portal.Logout(c.Request().Context(), cookie.Value); err != nil {
				return userPortalSessionError(err)
			}
		}
	}
	secure := h.portalCookieSecure(c.Request().Context())
	clearUserPortalCookie(c, service.UserPortalSessionCookieName, true, secure)
	clearUserPortalCookie(c, service.UserPortalCSRFCookieName, false, secure)
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "signed_out"}})
}

func validateUserPortalLogoutCSRF(c echo.Context) error {
	headerToken := strings.TrimSpace(c.Request().Header.Get(service.SSOCSRFHeaderName))
	cookie, err := c.Request().Cookie(service.UserPortalCSRFCookieName)
	if err != nil || headerToken == "" || strings.TrimSpace(cookie.Value) == "" {
		return httperr.New(httperr.FORBIDDEN, "invalid user portal csrf token")
	}
	if subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookie.Value)) != 1 {
		return httperr.New(httperr.FORBIDDEN, "invalid user portal csrf token")
	}
	return nil
}

func (h *userPortalHandler) portalCookieSecure(ctx context.Context) bool {
	if h.sso == nil {
		return false
	}
	return h.sso.CookieSecure(ctx)
}

func setUserPortalCookie(c echo.Context, name, value string, httpOnly, remember bool, expires time.Time, secure bool) {
	cookie := &nethttp.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/ui",
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: nethttp.SameSiteLaxMode,
	}
	if remember {
		cookie.Expires = expires
		cookie.MaxAge = int(time.Until(expires).Seconds())
	}
	c.SetCookie(cookie)
}

func clearUserPortalCookie(c echo.Context, name string, httpOnly, secure bool) {
	c.SetCookie(&nethttp.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/ui",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: nethttp.SameSiteLaxMode,
	})
}

func userPortalSessionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, service.ErrUserPortalSessionInvalid):
		return httperr.New(httperr.AUTH_INVALID, "invalid user portal session")
	case errors.Is(err, service.ErrUserPortalCSRFInvalid):
		return httperr.New(httperr.FORBIDDEN, "invalid user portal csrf token")
	default:
		return httperr.New(httperr.INTERNAL_ERROR, "user portal session failed")
	}
}
