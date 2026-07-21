package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

// V2RecallHandler serves the legacy GET /api/v1/recall HTTP shape from the V2
// recall pipeline for UAT/browser compatibility.
type V2RecallHandler struct {
	svc memoryservice.V2RecallService
}

var _ RecallHandlerInterface = (*V2RecallHandler)(nil)

func NewV2RecallHandler(svc memoryservice.V2RecallService) *V2RecallHandler {
	return &V2RecallHandler{svc: svc}
}

func (h *V2RecallHandler) Handle(c echo.Context) error {
	ctx := c.Request().Context()
	teamID, ok := middleware.GetResolvedTeamID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "profile ID is required")
	}
	if h == nil || h.svc == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "recall unavailable")
	}

	var req dto.RecallRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid query parameters")
	}
	if err := validation.ValidateStruct(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	result, err := h.svc.RecallV2(ctx, memoryservice.V2RecallRequest{
		ContractVersion: domain.V2ContractVersion,
		Query:           req.Query,
		Limit:           req.Limit,
		ValidAt:         req.ValidAt,
		KnownAt:         req.KnownAt,
	})
	if err != nil {
		if errors.Is(err, memoryservice.ErrV2RecallAuthContext) {
			return httperr.New(httperr.FORBIDDEN, "authentication required")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "recall failed")
	}
	if result != nil && result.Degradation != nil && result.Degradation.RequiredFailure {
		message := strings.TrimSpace(result.Degradation.Message)
		if message == "" {
			message = "recall unavailable"
		}
		return httperr.New(httperr.SERVICE_UNAVAILABLE, message)
	}

	items := v2RecallHTTPItems(teamID.String(), result)
	return response.SuccessOK(c, items)
}

func v2RecallHTTPItems(teamID string, result *memoryservice.V2RecallResult) []dto.RecallHitResponse {
	if result == nil {
		return []dto.RecallHitResponse{}
	}
	items := make([]dto.RecallHitResponse, 0, len(result.Results))
	for _, hit := range result.Results {
		score := 0.0
		if hit.Rank > 0 {
			score = 1 / float64(hit.Rank)
		}
		metadata := map[string]any{
			"relationship_ids": hit.RelationshipIDs,
			"search_state":     result.SearchState,
			"v2_recall_id":     result.RecallID,
		}
		items = append(items, dto.RecallHitResponse{
			Tier:       recallservice.TierFragment,
			Score:      score,
			FinalScore: score,
			Fragment: &dto.FragmentResponse{
				ID:         hit.EvidenceID,
				FragmentID: hit.EvidenceID,
				TeamID:     teamID,
				Content:    hit.Context,
				SourceType: string(domain.SourceTypeObservation),
				Source:     "v2 recall",
				Metadata:   metadata,
				Status:     string(domain.FragmentStatusActive),
			},
		})
	}
	return items
}
