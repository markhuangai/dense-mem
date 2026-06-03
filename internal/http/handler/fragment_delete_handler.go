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

// FragmentDeleteHandler serves DELETE /api/v1/fragments/:id.
type FragmentDeleteHandler struct {
	svc fragmentservice.DeleteFragmentService
}

// FragmentDeleteHandlerInterface is the companion interface for FragmentDeleteHandler.
type FragmentDeleteHandlerInterface interface {
	Handle(c echo.Context) error
}

var _ FragmentDeleteHandlerInterface = (*FragmentDeleteHandler)(nil)

// NewFragmentDeleteHandler constructs a FragmentDeleteHandler.
func NewFragmentDeleteHandler(svc fragmentservice.DeleteFragmentService) *FragmentDeleteHandler {
	return &FragmentDeleteHandler{svc: svc}
}

// Handle hard-deletes a fragment within the caller's profile scope (AC-31).
// Missing fragments and cross-profile deletes both return 404 so existence is
// not leaked across profiles. Success returns 204 No Content.
func (h *FragmentDeleteHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	fragmentID := c.Param("id")
	if fragmentID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "fragment id is required")
	}

	if err := h.svc.Delete(ctx, profileID.String(), fragmentID); err != nil {
		if errors.Is(err, fragmentservice.ErrFragmentNotFound) {
			return httperr.New(httperr.NOT_FOUND, "fragment not found")
		}
		if errors.Is(err, ownership.ErrOwnerMismatch) {
			return httperr.New(httperr.FORBIDDEN, "only the owner profile can modify this knowledge")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "failed to delete fragment")
	}

	return c.NoContent(http.StatusNoContent)
}
