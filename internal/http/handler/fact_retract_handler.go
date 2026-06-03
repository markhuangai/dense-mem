package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

// FactRetractHandler serves POST /api/v1/facts/:id/retract.
type FactRetractHandler struct {
	svc factservice.RetractFactService
}

// NewFactRetractHandler constructs a FactRetractHandler.
func NewFactRetractHandler(svc factservice.RetractFactService) *FactRetractHandler {
	return &FactRetractHandler{svc: svc}
}

// Handle soft-tombstones the fact identified by :id within the caller's profile.
func (h *FactRetractHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	factID := c.Param("id")
	if factID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "fact id is required")
	}

	if err := h.svc.Retract(ctx, profileID.String(), factID); err != nil {
		if errors.Is(err, factservice.ErrFactNotFound) {
			return httperr.New(httperr.ErrFactNotFound, "fact not found")
		}
		if errors.Is(err, ownership.ErrOwnerMismatch) {
			return httperr.New(httperr.FORBIDDEN, "only the owner profile can modify this knowledge")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "failed to retract fact")
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "retracted"})
}
