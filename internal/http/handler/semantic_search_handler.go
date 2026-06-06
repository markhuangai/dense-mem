package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

// SemanticSearchHandler handles HTTP requests for semantic_search operations.
type SemanticSearchHandler struct {
	svc semanticsearch.SemanticSearchService
}

// SemanticSearchHandlerInterface is the companion interface for SemanticSearchHandler.
type SemanticSearchHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure SemanticSearchHandler implements SemanticSearchHandlerInterface.
var _ SemanticSearchHandlerInterface = (*SemanticSearchHandler)(nil)

// NewSemanticSearchHandler creates a new semantic search handler.
func NewSemanticSearchHandler(svc semanticsearch.SemanticSearchService) *SemanticSearchHandler {
	return &SemanticSearchHandler{svc: svc}
}

// Handle handles POST /api/v1/tools/semantic_search.
// It validates the request, executes the search, and returns results.
func (h *SemanticSearchHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	// Get resolved profile ID from context (set by ProfileResolutionMiddleware)
	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	// Bind request body to the shared DTO so the public HTTP contract and
	// validation rules remain aligned with the catalog/OpenAPI schema.
	var req dto.SemanticSearchRequest
	if err := c.Bind(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, "malformed JSON body")
	}

	if err := validation.ValidateStruct(&req); err != nil {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Execute search
	result, err := h.svc.Search(ctx, profileID.String(), &semanticsearch.SemanticSearchRequest{
		Embedding: req.Embedding,
		Query:     req.Query,
		Limit:     req.Limit,
		Threshold: req.Threshold,
	})
	if err != nil {
		return handleSemanticSearchError(err)
	}

	// Return 200 with results
	return c.JSON(http.StatusOK, result)
}

// handleSemanticSearchError converts service errors to HTTP errors.
func handleSemanticSearchError(err error) *httperr.APIError {
	if err == nil {
		return nil
	}

	// Check for embedding generation not configured error (501)
	if semanticsearch.IsEmbeddingGenerationNotConfiguredError(err) {
		return httperr.New(httperr.EMBEDDING_GENERATION_NOT_CONFIGURED, err.Error())
	}

	// Check for dimension mismatch error (422)
	if semanticsearch.IsDimensionMismatchError(err) {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Check for validation errors (422)
	if semanticsearch.IsValidationError(err) {
		return httperr.New(httperr.VALIDATION_ERROR, err.Error())
	}

	// Default to internal error
	return httperr.New(httperr.INTERNAL_ERROR, "failed to execute search")
}
