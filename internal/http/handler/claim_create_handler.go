package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
)

// ClaimCreateHandler handles HTTP requests for claim creation.
type ClaimCreateHandler struct {
	svc claimservice.CreateClaimService
}

// ClaimCreateHandlerInterface is the companion interface for ClaimCreateHandler.
type ClaimCreateHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure ClaimCreateHandler implements ClaimCreateHandlerInterface.
var _ ClaimCreateHandlerInterface = (*ClaimCreateHandler)(nil)

// NewClaimCreateHandler creates a new claim create handler.
func NewClaimCreateHandler(svc claimservice.CreateClaimService) *ClaimCreateHandler {
	return &ClaimCreateHandler{svc: svc}
}

// Handle handles POST /api/v1/claims.
// It validates the request, creates the claim, and returns the result.
// Returns 201 Created for new claims, 200 OK with X-Idempotent-Replay header for duplicates.
func (h *ClaimCreateHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	// Get resolved profile ID from context (set by ProfileResolutionMiddleware).
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	// Bind request body to DTO.
	var req dto.CreateClaimRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}

	// Validate DTO (including cross-field date check registered in dto.init).
	if err := validation.ValidateStruct(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Map DTO fields to the domain model.
	// ProfileID is set here and repeated in the service call for isolation enforcement.
	claim := &domain.Claim{
		ProfileID:      profileID.String(),
		SupportedBy:    req.SupportedBy,
		Subject:        req.Subject,
		Predicate:      req.Predicate,
		Object:         req.Object,
		Modality:       domain.ClaimModality(req.Modality),
		Polarity:       domain.ClaimPolarity(req.Polarity),
		Speaker:        req.Speaker,
		ExtractConf:    req.ExtractConf,
		ResolutionConf: req.ResolutionConf,
		IdempotencyKey: req.IdempotencyKey,
		ValidFrom:      req.ValidFrom,
		ValidTo:        req.ValidTo,
	}

	result, err := h.svc.Create(ctx, profileID.String(), claim)
	if err != nil {
		var apiErr *httperr.APIError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		if errors.Is(err, claimservice.ErrSupportingFragmentMissing) {
			return httperr.New(httperr.ErrSupportingFragmentMissing, "supporting fragment missing or retracted")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "failed to create claim")
	}

	resp := response.ToClaimResponse(result.Claim, result.DuplicateOf)

	if result.Duplicate {
		c.Response().Header().Set("X-Idempotent-Replay", "true")
		return c.JSON(http.StatusOK, resp)
	}

	return c.JSON(http.StatusCreated, resp)
}
