package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

// ClaimVerifyHandler serves POST /api/v1/claims/:id/verify.
// It runs entailment verification for the claim and returns the updated claim state.
type ClaimVerifyHandler struct {
	svc claimservice.VerifyClaimService
}

// ClaimVerifyHandlerInterface is the companion interface for ClaimVerifyHandler.
type ClaimVerifyHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure ClaimVerifyHandler implements ClaimVerifyHandlerInterface.
var _ ClaimVerifyHandlerInterface = (*ClaimVerifyHandler)(nil)

// NewClaimVerifyHandler constructs a ClaimVerifyHandler.
func NewClaimVerifyHandler(svc claimservice.VerifyClaimService) *ClaimVerifyHandler {
	return &ClaimVerifyHandler{svc: svc}
}

// Handle runs entailment verification for the claim identified by :id,
// scoped to the caller's profile. Maps verifier sentinels to their
// corresponding HTTP status codes and preserves Retry-After on 429.
func (h *ClaimVerifyHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	claimID := c.Param("id")
	if claimID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "claim id is required")
	}

	claim, err := h.svc.Verify(ctx, profileID.String(), claimID)
	if err != nil {
		var apiErr *httperr.APIError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		switch {
		case errors.Is(err, claimservice.ErrClaimNotFound):
			return httperr.New(httperr.ErrClaimNotFound, "claim not found")
		case errors.Is(err, ownership.ErrOwnerMismatch):
			return httperr.New(httperr.FORBIDDEN, "only the owner profile can modify this knowledge")
		case errors.Is(err, claimservice.ErrSupportingFragmentMissing):
			return httperr.New(httperr.ErrSupportingFragmentMissing, "supporting fragment missing or retracted")

		case errors.Is(err, verifier.ErrVerifierRateLimit):
			// Preserve Retry-After when the provider supplies one.
			var rlErr *verifier.RateLimitError
			if errors.As(err, &rlErr) && rlErr.RetryAfter > 0 {
				c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", rlErr.RetryAfter))
			}
			return httperr.New(httperr.ErrVerifierRateLimit, "verifier rate limited; retry later")

		case errors.Is(err, verifier.ErrVerifierTimeout):
			return httperr.New(httperr.ErrVerifierTimeout, "verifier request timed out")

		case errors.Is(err, verifier.ErrVerifierProvider):
			return httperr.New(httperr.ErrVerifierProvider, "verifier provider error")

		case errors.Is(err, verifier.ErrVerifierMalformedResponse):
			return httperr.New(httperr.ErrVerifierMalformedResponse, "verifier returned a malformed response")

		default:
			return httperr.New(httperr.INTERNAL_ERROR, "failed to verify claim")
		}
	}

	resp := &dto.VerifyClaimResponse{
		ClaimID:              claim.ClaimID,
		EntailmentVerdict:    string(claim.EntailmentVerdict),
		Status:               string(claim.Status),
		LastVerifierResponse: claim.LastVerifierResponse,
		VerifierModel:        claim.VerifierModel,
		VerifiedAt:           claim.VerifiedAt,
	}

	return c.JSON(http.StatusOK, resp)
}
