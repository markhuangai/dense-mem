package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
)

// FragmentRetractHandler serves POST /api/v1/fragments/:id/retract.
// Retract is a soft tombstone: the fragment node is preserved in the graph for
// lineage, but its status is set to "retracted" and it is excluded from all
// active-fragment reads. Facts whose remaining active support falls below the
// promotion gate are marked "needs_revalidation". Hard delete remains a
// separate operation (DELETE /api/v1/fragments/:id).
type FragmentRetractHandler struct {
	svc fragmentservice.RetractFragmentService
}

// FragmentRetractHandlerInterface is the companion interface for FragmentRetractHandler.
type FragmentRetractHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure FragmentRetractHandler implements FragmentRetractHandlerInterface.
var _ FragmentRetractHandlerInterface = (*FragmentRetractHandler)(nil)

// NewFragmentRetractHandler constructs a FragmentRetractHandler.
func NewFragmentRetractHandler(svc fragmentservice.RetractFragmentService) *FragmentRetractHandler {
	return &FragmentRetractHandler{svc: svc}
}

// Handle tombstones the fragment identified by :id within the caller's profile
// scope. Missing fragments and cross-profile retracts both return 404 so
// existence is not leaked across profiles. Success returns 200
// {"status":"retracted"}.
func (h *FragmentRetractHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	fragmentID := c.Param("id")
	if fragmentID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "fragment id is required")
	}

	if err := h.svc.Retract(ctx, profileID.String(), fragmentID); err != nil {
		if errors.Is(err, fragmentservice.ErrFragmentNotFound) {
			return httperr.New(httperr.NOT_FOUND, "fragment not found")
		}
		if errors.Is(err, ownership.ErrOwnerMismatch) {
			return httperr.New(httperr.FORBIDDEN, "only the owner profile can modify this knowledge")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "failed to retract fragment")
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "retracted"})
}
