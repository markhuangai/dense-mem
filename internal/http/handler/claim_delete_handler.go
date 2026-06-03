package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
)

// ClaimDeleteHandler serves DELETE /api/v1/claims/:id.
type ClaimDeleteHandler struct {
	svc claimservice.DeleteClaimService
}

// ClaimDeleteHandlerInterface is the companion interface for ClaimDeleteHandler.
type ClaimDeleteHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure ClaimDeleteHandler implements ClaimDeleteHandlerInterface.
var _ ClaimDeleteHandlerInterface = (*ClaimDeleteHandler)(nil)

// NewClaimDeleteHandler constructs a ClaimDeleteHandler.
func NewClaimDeleteHandler(svc claimservice.DeleteClaimService) *ClaimDeleteHandler {
	return &ClaimDeleteHandler{svc: svc}
}

// Handle permanently removes a claim within the caller's profile scope.
// Missing claims and cross-profile deletes both return 404 so existence is
// not leaked across profiles. Success returns 200 {"status":"deleted"}.
func (h *ClaimDeleteHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	claimID := c.Param("id")
	if claimID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "claim id is required")
	}

	if err := h.svc.Delete(ctx, profileID.String(), claimID); err != nil {
		if errors.Is(err, claimservice.ErrClaimNotFound) {
			return httperr.New(httperr.ErrClaimNotFound, "claim not found")
		}
		if errors.Is(err, ownership.ErrOwnerMismatch) {
			return httperr.New(httperr.FORBIDDEN, "only the owner profile can modify this knowledge")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "failed to delete claim")
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "deleted"})
}
