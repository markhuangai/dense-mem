package http

import (
	"context"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlPortalActorContextKey struct{}

func controlPortalActorFromContext(ctx context.Context) string {
	actor, ok := ctx.Value(controlPortalActorContextKey{}).(string)
	if !ok || strings.TrimSpace(actor) == "" {
		return "control_portal"
	}
	return actor
}

func controlPortalActorFromRequest(req *nethttp.Request) string {
	if strings.TrimSpace(req.Header.Get("X-Control-Portal-Token")) != "" {
		return "control_portal:x-control-portal-token"
	}
	if strings.HasPrefix(strings.TrimSpace(req.Header.Get(echo.HeaderAuthorization)), "Bearer ") {
		return "control_portal:authorization-bearer"
	}
	return "control_portal"
}

func controlPortalMiddleware(token string, securitySvc service.SecurityService, controlIdentity ...*service.ControlIdentityService) echo.MiddlewareFunc {
	var identityService *service.ControlIdentityService
	if len(controlIdentity) > 0 {
		identityService = controlIdentity[0]
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			origin := c.Request().Header.Get(echo.HeaderOrigin)
			if origin != "" {
				requestedOrigin := controlPortalOrigin(origin)
				allowedOrigin, bootstrap := controlPortalConfiguredOrigin(c.Request().Context(), identityService)
				if allowedOrigin == "" && bootstrap {
					allowedOrigin = controlPortalRequestOrigin(c.Request())
				}
				if requestedOrigin == "" || allowedOrigin == "" || requestedOrigin != allowedOrigin {
					return httperr.New(httperr.FORBIDDEN, "control portal origin is not allowed")
				}
				c.Response().Header().Set(echo.HeaderVary, echo.HeaderOrigin)
				c.Response().Header().Set(echo.HeaderAccessControlAllowOrigin, origin)
				c.Response().Header().Set(echo.HeaderAccessControlAllowCredentials, "true")
				c.Response().Header().Set(echo.HeaderAccessControlAllowHeaders, "Authorization, Content-Type, X-Control-Portal-Token, "+service.ControlCSRFHeaderName)
				c.Response().Header().Set(echo.HeaderAccessControlAllowMethods, "GET, POST, PATCH, DELETE, OPTIONS")
			}
			if c.Request().Method == nethttp.MethodOptions {
				return c.NoContent(nethttp.StatusNoContent)
			}
			actor := ""
			if controlTokenMatches(c.Request(), token) {
				actor = controlPortalActorFromRequest(c.Request())
			} else if identityService != nil {
				cookie, err := c.Cookie(service.ControlSessionCookieName)
				if err == nil {
					csrf := c.Request().Header.Get(service.ControlCSRFHeaderName)
					requireCSRF := c.Request().Method != nethttp.MethodGet && c.Request().Method != nethttp.MethodHead
					if _, authErr := identityService.AuthenticateSession(c.Request().Context(), cookie.Value, csrf, requireCSRF); authErr == nil {
						actor = "control_portal:sso"
					}
				}
			}
			if actor == "" {
				recordControlAuthFailure(c, securitySvc)
				return httperr.New(httperr.AUTH_INVALID, "invalid control portal credentials")
			}
			ctx := context.WithValue(c.Request().Context(), controlPortalActorContextKey{}, actor)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

func controlPortalAllowedOrigin(ctx context.Context, identityService *service.ControlIdentityService) string {
	origin, _ := controlPortalConfiguredOrigin(ctx, identityService)
	return origin
}

func controlPortalConfiguredOrigin(ctx context.Context, identityService *service.ControlIdentityService) (string, bool) {
	if identityService == nil {
		return "", false
	}
	baseURL, err := identityService.ControlPublicBaseURL(ctx)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(baseURL) == "" {
		return "", true
	}
	return controlPortalOrigin(baseURL), false
}

func controlPortalRequestOrigin(request *nethttp.Request) string {
	if request == nil {
		return ""
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded != "" {
		scheme = forwarded
	}
	host := request.Host
	if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Host"), ",")[0]); forwarded != "" {
		host = forwarded
	}
	return controlPortalOrigin(scheme + "://" + host)
}

func controlPortalOrigin(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := parsed.Port()
	if port != "" && !(scheme == "http" && port == "80") && !(scheme == "https" && port == "443") {
		host += ":" + port
	}
	return scheme + "://" + host
}

func recordControlAuthFailure(c echo.Context, securitySvc service.SecurityService) {
	if securitySvc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := securitySvc.RecordAuthFailure(ctx, c.RealIP(), "control", "AUTH_INVALID"); err != nil {
		c.Logger().Errorf("control security auth failure record failed: %v", err)
	}
}
