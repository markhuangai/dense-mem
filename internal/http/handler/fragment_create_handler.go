package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/embedding"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
)

// FragmentCreateHandler handles HTTP requests for fragment creation.
type FragmentCreateHandler struct {
	svc fragmentservice.CreateFragmentService
}

// FragmentCreateHandlerInterface is the companion interface for FragmentCreateHandler.
type FragmentCreateHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure FragmentCreateHandler implements FragmentCreateHandlerInterface.
var _ FragmentCreateHandlerInterface = (*FragmentCreateHandler)(nil)

// NewFragmentCreateHandler creates a new fragment create handler.
func NewFragmentCreateHandler(svc fragmentservice.CreateFragmentService) *FragmentCreateHandler {
	return &FragmentCreateHandler{svc: svc}
}

// Handle handles POST /api/v1/fragments.
// It validates the request, creates the fragment with embedding, and returns the result.
// Returns 201 Created for new fragments, 200 OK with X-Idempotent-Replay header for duplicates.
func (h *FragmentCreateHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	// Get resolved profile ID from context (set by ProfileResolutionMiddleware)
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	// Bind request body to DTO
	var req dto.CreateFragmentRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}

	// Validate DTO
	if err := validation.ValidateStruct(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Validate metadata size (cannot be done via struct tag)
	if err := dto.ValidateMetadataSize(req.Metadata); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Call service
	result, err := h.svc.Create(ctx, profileID.String(), &req)
	if err != nil {
		return handleFragmentCreateError(ctx, err)
	}

	// Convert to response (embedding vector excluded - AC-28)
	resp := response.ToFragmentResponse(result.Fragment)

	// Check for idempotent replay
	if result.Duplicate {
		c.Response().Header().Set("X-Idempotent-Replay", "true")
		return c.JSON(http.StatusOK, resp)
	}

	// Return 201 Created for new fragment
	return c.JSON(http.StatusCreated, resp)
}

// handleFragmentCreateError converts service errors to HTTP errors.
func handleFragmentCreateError(ctx context.Context, err error) *httperr.APIError {
	if err == nil {
		return nil
	}
	var apiErr *httperr.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	// Check for embedding timeout errors → 504 Gateway Timeout
	if errors.Is(err, embedding.ErrEmbeddingTimeout) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding request timed out")
	}

	// Check for embedding provider errors → 503 Service Unavailable
	if errors.Is(err, embedding.ErrEmbeddingProvider) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding service unavailable")
	}

	// Check for embedding rate limit errors → 503 Service Unavailable
	if errors.Is(err, embedding.ErrEmbeddingRateLimit) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding service rate limited")
	}

	// Check for embedding failed from the service → 503 Service Unavailable
	if errors.Is(err, fragmentservice.ErrEmbeddingFailed) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding service unavailable")
	}

	// Check for context deadline exceeded → 504 Gateway Timeout
	if errors.Is(err, context.DeadlineExceeded) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "request timed out")
	}

	// Default to internal error
	return httperr.New(httperr.INTERNAL_ERROR, "failed to create fragment")
}
