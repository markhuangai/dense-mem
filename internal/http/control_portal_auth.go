package http

import (
	"context"
	nethttp "net/http"
	"strings"

	"github.com/labstack/echo/v4"
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
