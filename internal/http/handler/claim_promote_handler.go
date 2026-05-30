package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

// ClaimPromoteHandler handles HTTP requests for promoting a validated claim to a fact.
type ClaimPromoteHandler struct {
	svc factservice.PromoteClaimService
}

// ClaimPromoteHandlerInterface is the companion interface for ClaimPromoteHandler.
type ClaimPromoteHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure ClaimPromoteHandler implements ClaimPromoteHandlerInterface.
var _ ClaimPromoteHandlerInterface = (*ClaimPromoteHandler)(nil)

// NewClaimPromoteHandler creates a new promote handler.
func NewClaimPromoteHandler(svc factservice.PromoteClaimService) *ClaimPromoteHandler {
	return &ClaimPromoteHandler{svc: svc}
}

// Handle handles POST /api/v1/claims/:id/promote.
// Returns 201 Created with the newly promoted Fact on success.
// Maps promotion-specific errors (U35/U41/U42) to their public error codes.
func (h *ClaimPromoteHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	// Get resolved profile ID from context (set by ProfileResolutionMiddleware).
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	claimID := c.Param("id")
	if claimID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "claim id is required")
	}

	fact, err := h.svc.Promote(ctx, profileID.String(), claimID)
	if err != nil {
		var apiErr *httperr.APIError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		switch {
		case errors.Is(err, factservice.ErrClaimNotFound):
			return httperr.New(httperr.ErrClaimNotFound, "claim not found")
		case errors.Is(err, factservice.ErrPredicateNotPoliced):
			return httperr.New(httperr.ErrPredicateNotPoliced, "predicate not policed for promotion")
		case errors.Is(err, factservice.ErrUnsupportedPolicy):
			return httperr.New(httperr.ErrUnsupportedPolicy, "unsupported promotion policy")
		case errors.Is(err, factservice.ErrClaimNotValidated):
			return httperr.New(httperr.ErrNeedsClaimValidated, "claim must be validated before promotion")
		case errors.Is(err, factservice.ErrGateRejected):
			return httperr.New(httperr.ErrGateRejected, "claim did not meet promotion gate thresholds")
		case errors.Is(err, factservice.ErrPromotionDeferredDisputed):
			return httperr.New(httperr.ErrComparableDisputed, "promotion deferred: comparable fact exists")
		case errors.Is(err, factservice.ErrPromotionRejected):
			return httperr.New(httperr.ErrRejectedWeaker, "promotion rejected: claim weaker than existing fact")
		default:
			return httperr.New(httperr.INTERNAL_ERROR, "failed to promote claim")
		}
	}

	resp := response.ToFactResponse(fact)
	return c.JSON(http.StatusCreated, resp)
}
