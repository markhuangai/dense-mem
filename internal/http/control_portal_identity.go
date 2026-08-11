package http

import (
	"crypto/subtle"
	"errors"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func registerControlIdentityRoutes(e *echo.Echo, h *controlPortalHandler) {
	if e == nil || h == nil || h.controlIdentity == nil {
		return
	}
	auth := e.Group("/control/auth")
	auth.GET("/providers", h.controlIdentityProviders)
	auth.GET("/start/:providerId", h.startControlIdentityLogin)
	auth.GET("/callback", h.completeControlIdentityLogin)
	auth.POST("/logout", h.logoutControlIdentity)
}

func (h *controlPortalHandler) controlIdentityProviders(c echo.Context) error {
	providers, err := h.controlIdentity.ListEnabledProviders(c.Request().Context())
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	items := make([]controlIdentityProviderResponse, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			items = append(items, controlIdentityProviderResponse{ID: provider.ID, Name: provider.Name, Kind: string(provider.Kind)})
		}
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": items})
}

func (h *controlPortalHandler) startControlIdentityLogin(c echo.Context) error {
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	callbackURL, err := controlIdentityCallbackURL(c, h.controlIdentity)
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	start, err := h.controlIdentity.BeginLogin(c.Request().Context(), providerID, callbackURL)
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	return c.Redirect(nethttp.StatusFound, start.AuthURL)
}

func (h *controlPortalHandler) completeControlIdentityLogin(c echo.Context) error {
	if strings.TrimSpace(c.QueryParam("error")) != "" {
		return httperr.New(httperr.AUTH_INVALID, "control sign-in was denied")
	}
	callbackURL, err := controlIdentityCallbackURL(c, h.controlIdentity)
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	result, err := h.controlIdentity.CompleteLogin(c.Request().Context(), c.QueryParam("state"), c.QueryParam("code"), callbackURL)
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	maxAge := int(time.Until(result.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	secure := h.controlIdentity.CookieSecure(c.Request().Context())
	nethttp.SetCookie(c.Response(), &nethttp.Cookie{
		Name:     service.ControlSessionCookieName,
		Value:    result.SessionToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: nethttp.SameSiteLaxMode,
	})
	nethttp.SetCookie(c.Response(), &nethttp.Cookie{
		Name:     service.ControlCSRFCookieName,
		Value:    result.CSRFToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   secure,
		SameSite: nethttp.SameSiteLaxMode,
	})
	return c.Redirect(nethttp.StatusFound, "/")
}

func (h *controlPortalHandler) logoutControlIdentity(c echo.Context) error {
	secure := h.controlIdentity.CookieSecure(c.Request().Context())

	var sessionCookie *nethttp.Cookie
	if cookie, err := c.Cookie(service.ControlSessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		sessionCookie = cookie
		csrfCookie, csrfErr := c.Cookie(service.ControlCSRFCookieName)
		csrfHeader := strings.TrimSpace(c.Request().Header.Get(service.ControlCSRFHeaderName))
		if csrfErr != nil || strings.TrimSpace(csrfCookie.Value) == "" || csrfHeader == "" || subtle.ConstantTimeCompare([]byte(csrfHeader), []byte(csrfCookie.Value)) != 1 {
			return controlIdentityHTTPError(service.ErrControlCSRFInvalid)
		}
		if _, authErr := h.controlIdentity.AuthenticateSession(c.Request().Context(), sessionCookie.Value, csrfHeader, true); authErr != nil {
			return controlIdentityHTTPError(authErr)
		}
	}

	var logoutErr error
	if sessionCookie != nil {
		logoutErr = h.controlIdentity.Logout(c.Request().Context(), sessionCookie.Value)
	}
	if logoutErr != nil {
		return controlIdentityHTTPError(logoutErr)
	}
	clearControlIdentityCookies(c, secure)
	return c.NoContent(nethttp.StatusNoContent)
}

func clearControlIdentityCookies(c echo.Context, secure bool) {
	nethttp.SetCookie(c.Response(), &nethttp.Cookie{Name: service.ControlSessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: nethttp.SameSiteLaxMode})
	nethttp.SetCookie(c.Response(), &nethttp.Cookie{Name: service.ControlSessionCookieName, Value: "", Path: "/control", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: nethttp.SameSiteLaxMode})
	nethttp.SetCookie(c.Response(), &nethttp.Cookie{Name: service.ControlCSRFCookieName, Value: "", Path: "/control", MaxAge: -1, HttpOnly: false, Secure: secure, SameSite: nethttp.SameSiteLaxMode})
	nethttp.SetCookie(c.Response(), &nethttp.Cookie{Name: service.ControlCSRFCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: false, Secure: secure, SameSite: nethttp.SameSiteLaxMode})
}

func (h *controlPortalHandler) listControlAdminGroups(c echo.Context) error {
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	groups, err := h.controlIdentity.ListAdminGroups(c.Request().Context(), providerID)
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	items := make([]controlAdminGroupResponse, 0, len(groups))
	for _, group := range groups {
		items = append(items, toControlAdminGroup(group))
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": items})
}

func (h *controlPortalHandler) createControlAdminGroup(c echo.Context) error {
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	var body controlAdminGroupRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	group, err := h.controlIdentity.CreateAdminGroup(c.Request().Context(), body.toDomain(uuid.Nil, providerID))
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	return c.JSON(nethttp.StatusCreated, map[string]any{"data": toControlAdminGroup(group)})
}

func (h *controlPortalHandler) updateControlAdminGroup(c echo.Context) error {
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	groupID, err := parseControlUUID(c.Param("groupId"), "control admin group ID")
	if err != nil {
		return err
	}
	var body controlAdminGroupRequest
	if err := c.Bind(&body); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	group, err := h.controlIdentity.UpdateAdminGroup(c.Request().Context(), body.toDomain(groupID, providerID))
	if err != nil {
		return controlIdentityHTTPError(err)
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlAdminGroup(group)})
}

func (h *controlPortalHandler) deleteControlAdminGroup(c echo.Context) error {
	providerID, err := parseControlUUID(c.Param("providerId"), "SSO provider ID")
	if err != nil {
		return err
	}
	groupID, err := parseControlUUID(c.Param("groupId"), "control admin group ID")
	if err != nil {
		return err
	}
	if err := h.controlIdentity.RetireAdminGroup(c.Request().Context(), providerID, groupID); err != nil {
		return controlIdentityHTTPError(err)
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]string{"status": "retired"}})
}

func controlIdentityCallbackURL(c echo.Context, identity *service.ControlIdentityService) (string, error) {
	if identity == nil {
		return "", service.ErrControlSSOUnavailable
	}
	baseURL, err := identity.ControlPublicBaseURL(c.Request().Context())
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(baseURL) == "" {
		return "", service.ErrControlSSOUnavailable
	}
	return strings.TrimRight(baseURL, "/") + "/control/auth/callback", nil
}

type controlIdentityProviderResponse struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
}

type controlAdminGroupRequest struct {
	GroupID   string `json:"group_id"`
	GroupName string `json:"group_name"`
	Enabled   *bool  `json:"enabled"`
}

func (r controlAdminGroupRequest) toDomain(id, providerID uuid.UUID) domain.ControlAdminGroup {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return domain.ControlAdminGroup{ID: id, ProviderID: providerID, GroupID: r.GroupID, GroupName: r.GroupName, Enabled: enabled}
}

type controlAdminGroupResponse struct {
	ID         uuid.UUID `json:"id"`
	ProviderID uuid.UUID `json:"provider_id"`
	GroupID    string    `json:"group_id"`
	GroupName  string    `json:"group_name"`
	Enabled    bool      `json:"enabled"`
	RetiredAt  *string   `json:"retired_at"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
}

func toControlAdminGroup(group *domain.ControlAdminGroup) controlAdminGroupResponse {
	if group == nil {
		return controlAdminGroupResponse{}
	}
	var retiredAt *string
	if group.RetiredAt != nil {
		formatted := group.RetiredAt.Format(time.RFC3339)
		retiredAt = &formatted
	}
	return controlAdminGroupResponse{
		ID:         group.ID,
		ProviderID: group.ProviderID,
		GroupID:    group.GroupID,
		GroupName:  group.GroupName,
		Enabled:    group.Enabled,
		RetiredAt:  retiredAt,
		CreatedAt:  group.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  group.UpdatedAt.Format(time.RFC3339),
	}
}

func controlIdentityHTTPError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, service.ErrControlCSRFInvalid) {
		return httperr.New(httperr.FORBIDDEN, "invalid control csrf token")
	}
	if errors.Is(err, service.ErrControlAccessDenied) || errors.Is(err, service.ErrControlSessionInvalid) {
		return httperr.New(httperr.AUTH_INVALID, "control sign-in is invalid or no longer authorized")
	}
	if errors.Is(err, service.ErrControlSSOUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "control sign-in is not configured")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "not found") {
		return httperr.New(httperr.NOT_FOUND, "control identity resource not found")
	}
	if strings.Contains(message, "control admin") || strings.Contains(message, "sso provider") {
		return httperr.New(httperr.VALIDATION_ERROR, "invalid control identity request")
	}
	return httperr.New(httperr.INTERNAL_ERROR, "control identity operation failed")
}
