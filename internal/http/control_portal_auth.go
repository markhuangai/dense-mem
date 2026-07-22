package http

import (
	"context"
	"crypto/subtle"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type controlPortalActorContextKey struct{}

func controlPortalMiddleware(token string, securitySvc service.SecurityService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			origin := c.Request().Header.Get(echo.HeaderOrigin)
			if origin != "" {
				c.Response().Header().Set(echo.HeaderVary, echo.HeaderOrigin)
				c.Response().Header().Set(echo.HeaderAccessControlAllowOrigin, origin)
				c.Response().Header().Set(echo.HeaderAccessControlAllowHeaders, "Authorization, Content-Type, X-Control-Portal-Token")
				c.Response().Header().Set(echo.HeaderAccessControlAllowMethods, "GET, POST, PATCH, DELETE, OPTIONS")
			}
			if c.Request().Method == nethttp.MethodOptions {
				return c.NoContent(nethttp.StatusNoContent)
			}
			if !controlTokenMatches(c.Request(), token) {
				recordControlAuthFailure(c, securitySvc)
				return httperr.New(httperr.AUTH_INVALID, "invalid control portal token")
			}
			ctx := context.WithValue(c.Request().Context(), controlPortalActorContextKey{}, controlPortalActorFromRequest(c.Request()))
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

func controlPortalActorFromRequest(req *nethttp.Request) string {
	if strings.TrimSpace(req.Header.Get("X-Control-Portal-Token")) != "" {
		return "control_portal:x-control-portal-token"
	}
	auth := strings.TrimSpace(req.Header.Get(echo.HeaderAuthorization))
	if strings.HasPrefix(auth, "Bearer ") {
		return "control_portal:authorization-bearer"
	}
	return "control_portal"
}

func controlPortalActorFromContext(ctx context.Context) string {
	if actor, ok := ctx.Value(controlPortalActorContextKey{}).(string); ok && strings.TrimSpace(actor) != "" {
		return actor
	}
	return "control_portal"
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

func controlTokenMatches(req *nethttp.Request, expected string) bool {
	got := req.Header.Get("X-Control-Portal-Token")
	if got == "" {
		auth := req.Header.Get(echo.HeaderAuthorization)
		if strings.HasPrefix(auth, "Bearer ") {
			got = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
