package http

import (
	"errors"
	nethttp "net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (h *controlPortalHandler) identityCleanupPreflight(c echo.Context) error {
	if h.identityCleanup == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "identity cleanup preflight unavailable")
	}
	report, err := h.identityCleanup.Preflight(c.Request().Context())
	if err != nil {
		if errors.Is(err, service.ErrIdentityCleanupPreflightUnavailable) {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "identity cleanup preflight unavailable")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "identity cleanup preflight failed")
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": report})
}
