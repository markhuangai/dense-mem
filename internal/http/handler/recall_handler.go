package handler

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

// RecallHandler serves GET /api/v1/recall.
//
// The endpoint runs hybrid semantic + keyword recall for the caller's profile
// and returns ranked hits spanning all knowledge-pipeline tiers:
//   - tier "1"   active facts (highest authority)
//   - tier "1.5" validated claims
//   - tier "2"   raw SourceFragments (RRF-ranked)
//
// Profile isolation invariant: the profileID is always taken from the
// authenticated context set by ProfileResolutionMiddleware — never from
// caller-supplied query parameters.
type RecallHandler struct {
	svc recallservice.RecallService
}

// RecallHandlerInterface is the companion interface for RecallHandler.
type RecallHandlerInterface interface {
	Handle(c echo.Context) error
}

// Ensure RecallHandler implements RecallHandlerInterface.
var _ RecallHandlerInterface = (*RecallHandler)(nil)

// NewRecallHandler constructs a RecallHandler.
func NewRecallHandler(svc recallservice.RecallService) *RecallHandler {
	return &RecallHandler{svc: svc}
}

// Handle handles GET /api/v1/recall.
// Query parameters: query (required, max 512), limit (optional, 0-50).
// Returns 200 with a {"data": [...]} body on success.
// Returns 400 when the query parameter is missing or invalid.
// Returns 503 when the embedding provider is unavailable.
func (h *RecallHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()

	profileID, ok := middleware.GetResolvedProfileID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}

	var req dto.RecallRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid query parameters")
	}
	// Return 400 (not 422) for missing/invalid query param — stable external contract.
	if err := validation.ValidateStruct(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	serviceReq := recallservice.RecallRequest{
		Query:                req.Query,
		Limit:                req.Limit,
		ValidAt:              req.ValidAt,
		KnownAt:              req.KnownAt,
		KnownEvidenceIDs:     append([]string(nil), req.KnownEvidenceIDs...),
		ExpandFromEntityIDs:  append([]string(nil), req.ExpandFromEntityIDs...),
		KnownRelationshipIDs: append([]string(nil), req.KnownRelationshipIDs...),
	}
	hits, err := h.svc.Recall(ctx, profileID.String(), serviceReq)
	if err != nil {
		var apiErr *httperr.APIError
		if errors.As(err, &apiErr) {
			return apiErr
		}
		if errors.Is(err, recallservice.ErrEmbeddingUnavailable) {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "embedding provider unavailable")
		}
		if errors.Is(err, recallservice.ErrKeywordUnavailable) {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "keyword search unavailable")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "recall failed")
	}

	publicResponse, err := recallservice.RenderPublicRecall(serviceReq, hits)
	if err != nil {
		return httperr.New(httperr.INTERNAL_ERROR, "recall response failed")
	}
	publicResponse.RecallID = "rec_" + uuid.NewString()

	return response.SuccessOK(c, publicResponse)
}
