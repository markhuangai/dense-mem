package http

import (
	"errors"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/conflictqueue"
)

func (h *controlPortalHandler) listConflictQueue(c echo.Context) error {
	if h.conflictQueue == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "conflict queue unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	limit := 0
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
		}
	}
	page, err := h.conflictQueue.List(c.Request().Context(), teamID.String(), conflictqueue.ListOptions{
		Status: c.QueryParam("status"),
		Limit:  limit,
		Cursor: c.QueryParam("cursor"),
	})
	if err != nil {
		return conflictQueueHTTPError(err)
	}
	return response.SuccessOK(c, page)
}

func conflictQueueHTTPError(err error) error {
	switch {
	case errors.Is(err, conflictqueue.ErrInvalidStatus):
		return httperr.New(httperr.VALIDATION_ERROR, "status must be open or overdue")
	case errors.Is(err, conflictqueue.ErrInvalidLimit):
		return httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
	case errors.Is(err, conflictqueue.ErrInvalidCursor):
		return httperr.New(httperr.VALIDATION_ERROR, "invalid cursor")
	case strings.Contains(err.Error(), "not configured"):
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "conflict queue unavailable")
	default:
		return httperr.New(httperr.INTERNAL_ERROR, "conflict queue unavailable")
	}
}
