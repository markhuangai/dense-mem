package http

import (
	nethttp "net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
)

func registerV2MigrationControlRoutes(api *echo.Group, control *controlPortalHandler) {
	api.GET("/v2/migration", control.getV2MigrationStatus)
}

func (h *controlPortalHandler) getV2MigrationStatus(c echo.Context) error {
	if h.migration == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "V2 authority status unavailable")
	}
	status, err := h.migration.Status(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": status})
}
