package http

import (
	nethttp "net/http"

	"github.com/labstack/echo/v4"
)

func (h *controlPortalHandler) session(c echo.Context) error {
	return c.JSON(nethttp.StatusOK, map[string]any{"data": map[string]bool{"authenticated": true}})
}
