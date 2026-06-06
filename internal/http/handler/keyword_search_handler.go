package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
)

// KeywordSearchHandler handles HTTP requests for keyword_search operations.
type KeywordSearchHandler struct {
	svc keywordsearch.KeywordSearchService
}

// KeywordSearchHandlerInterface is the companion interface for KeywordSearchHandler.
type KeywordSearchHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure KeywordSearchHandler implements KeywordSearchHandlerInterface.
var _ KeywordSearchHandlerInterface = (*KeywordSearchHandler)(nil)

// NewKeywordSearchHandler creates a new keyword search handler.
func NewKeywordSearchHandler(svc keywordsearch.KeywordSearchService) *KeywordSearchHandler {
	return &KeywordSearchHandler{svc: svc}
}

// Handle handles POST /api/v1/tools/keyword_search.
// It validates the request, executes the search, and returns results.
func (h *KeywordSearchHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	// Get resolved profile ID from context (set by ProfileResolutionMiddleware)
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	// Bind request body to DTO
	var req dto.KeywordSearchRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}

	// Validate DTO
	if err := validation.ValidateStruct(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Convert DTO to service request
	svcReq := &keywordsearch.KeywordSearchRequest{
		Query:   req.Keywords,
		Limit:   req.Limit,
		Labels:  req.Labels,
		ValidAt: req.ValidAt,
		KnownAt: req.KnownAt,
	}

	// Execute search
	result, err := h.svc.Search(ctx, profileID.String(), svcReq)
	if err != nil {
		return handleKeywordSearchError(err)
	}

	// Return 200 with results
	return c.JSON(http.StatusOK, result)
}

// handleKeywordSearchError converts service errors to HTTP errors.
func handleKeywordSearchError(err error) *httperr.APIError {
	if err == nil {
		return nil
	}

	// Check for validation errors
	if keywordsearch.IsValidationError(err) {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Default to internal error
	return httperr.New(httperr.INTERNAL_ERROR, "failed to execute search")
}
