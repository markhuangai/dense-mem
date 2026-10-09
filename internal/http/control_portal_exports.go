package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/audit"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

type AuditExportReader interface {
	ExportPage(context.Context, audit.ExportRequest) ([]byte, error)
}
type DiagnosticBundleReader interface {
	Bundle(context.Context) ([]byte, error)
}

func (h *controlPortalHandler) exportAudit(c echo.Context) error {
	query := c.QueryParams()
	for name, values := range query {
		if (name != "team_id" && name != "cursor" && name != "limit") || len(values) != 1 {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid audit export query")
		}
	}
	request := audit.ExportRequest{Cursor: c.QueryParam("cursor")}
	if raw, ok := query["team_id"]; ok {
		team, err := uuid.Parse(raw[0])
		if err != nil || team == uuid.Nil {
			return httperr.New(httperr.INVALID_UUID, "invalid team ID format")
		}
		request.TeamID = &team
	}
	if raw, ok := query["limit"]; ok {
		limit, err := strconv.Atoi(raw[0])
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid audit export limit")
		}
		request.Limit = limit
	}
	raw, err := h.auditExport.ExportPage(c.Request().Context(), request)
	if err != nil {
		if errors.Is(err, audit.ErrInvalidExport) {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid audit export cursor or scope")
		}
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "audit export unavailable")
	}
	return writeControlDownload(c, "application/x-ndjson", "audit.ndjson", raw)
}

func (h *controlPortalHandler) diagnosticBundle(c echo.Context) error {
	if len(c.QueryParams()) != 0 {
		return httperr.New(httperr.VALIDATION_ERROR, "diagnostic bundle does not accept query parameters")
	}
	raw, err := h.diagnostics.Bundle(c.Request().Context())
	if err != nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "diagnostic bundle unavailable")
	}
	return writeControlDownload(c, echo.MIMEApplicationJSON, "diagnostics.json", raw)
}

func writeControlDownload(c echo.Context, contentType, filename string, raw []byte) error {
	if err := c.Request().Context().Err(); err != nil {
		return err
	}
	controller := nethttp.NewResponseController(c.Response().Writer)
	if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, nethttp.ErrNotSupported) {
		return err
	}
	defer controller.SetWriteDeadline(time.Time{})
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return c.Blob(nethttp.StatusOK, contentType, raw)
}
