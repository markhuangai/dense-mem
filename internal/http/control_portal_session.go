package http

import (
	nethttp "net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

func (h *controlPortalHandler) session(c echo.Context) error {
	authMethod := "token"
	if strings.Contains(controlPortalActorFromContext(c.Request().Context()), ":sso") {
		authMethod = "sso"
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]any{"authenticated": true, "auth_method": authMethod}})
}
