package http

import (
	"errors"
	nethttp "net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
)

func registerV2MigrationControlRoutes(api *echo.Group, control *controlPortalHandler) {
	api.GET("/v2/migration", control.getV2MigrationStatus)
	api.POST("/v2/migration/preflight", control.approveV2MigrationPreflight)
	api.POST("/v2/migration/start", control.startV2Migration)
	api.POST("/v2/migration/pause", control.pauseV2Migration)
	api.POST("/v2/migration/resume", control.resumeV2Migration)
}

func (h *controlPortalHandler) getV2MigrationStatus(c echo.Context) error {
	if h.migration == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "v2 migration control unavailable")
	}
	status, err := h.migration.Status(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": status})
}

func (h *controlPortalHandler) approveV2MigrationPreflight(c echo.Context) error {
	return h.invokeV2MigrationAction(c, func(req migrationcontrol.OperatorRequest) (any, error) {
		return h.migration.ApprovePreflight(c.Request().Context(), req)
	})
}

func (h *controlPortalHandler) startV2Migration(c echo.Context) error {
	return h.invokeV2MigrationAction(c, func(req migrationcontrol.OperatorRequest) (any, error) {
		return h.migration.Start(c.Request().Context(), req)
	})
}

func (h *controlPortalHandler) pauseV2Migration(c echo.Context) error {
	return h.invokeV2MigrationAction(c, func(req migrationcontrol.OperatorRequest) (any, error) {
		return h.migration.Pause(c.Request().Context(), req)
	})
}

func (h *controlPortalHandler) resumeV2Migration(c echo.Context) error {
	return h.invokeV2MigrationAction(c, func(req migrationcontrol.OperatorRequest) (any, error) {
		return h.migration.Resume(c.Request().Context(), req)
	})
}

func (h *controlPortalHandler) invokeV2MigrationAction(
	c echo.Context,
	fn func(req migrationcontrol.OperatorRequest) (any, error),
) error {
	if h.migration == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "v2 migration control unavailable")
	}
	var req migrationcontrol.OperatorRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}
	if req.RemoteIP == "" {
		req.RemoteIP = c.RealIP()
	}
	res, err := fn(req)
	if err != nil {
		if migrationControlValidationError(err) {
			return httperr.New(httperr.VALIDATION_ERROR, err.Error())
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": res})
}

func migrationControlValidationError(err error) bool {
	return errors.Is(err, migrationcontrol.ErrIllegalTransition) ||
		errors.Is(err, migrationcontrol.ErrPreflightRequired) ||
		errors.Is(err, migrationcontrol.ErrAlreadyCutOver) ||
		errors.Is(err, migrationcontrol.ErrIncompatible)
}
