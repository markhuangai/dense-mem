package http

import (
	"context"
	nethttp "net/http"
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
