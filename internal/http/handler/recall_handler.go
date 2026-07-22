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
)

const recallTierEvidence = "2"

// RecallHandlerInterface is the companion interface for recall handlers.
type RecallHandlerInterface interface {
	Handle(c echo.Context) error
}

// RecallHandler serves GET /api/v1/recall from the active recall pipeline.
type RecallHandler struct {
	svc memoryservice.RecallService
}

var _ RecallHandlerInterface = (*RecallHandler)(nil)

func NewRecallHandler(svc memoryservice.RecallService) *RecallHandler {
	return &RecallHandler{svc: svc}
}

func (h *RecallHandler) Handle(c echo.Context) error {
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

	result, err := h.svc.Recall(ctx, memoryservice.V2RecallRequest{
		ContractVersion: domain.V2ContractVersion,
		Query:           req.Query,
		Limit:           req.Limit,
		ValidAt:         req.ValidAt,
		KnownAt:         req.KnownAt,
	})
	if err != nil {
		if errors.Is(err, memoryservice.ErrRecallAuthContext) {
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

	items := recallHTTPItems(teamID.String(), result)
	return response.SuccessOK(c, items)
}

func recallHTTPItems(teamID string, result *memoryservice.V2RecallResult) []dto.RecallHitResponse {
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
			"recall_id":        result.RecallID,
		}
		items = append(items, dto.RecallHitResponse{
			Tier:       recallTierEvidence,
			Score:      score,
			FinalScore: score,
			Fragment: &dto.FragmentResponse{
				ID:         hit.EvidenceID,
				FragmentID: hit.EvidenceID,
				TeamID:     teamID,
				Content:    hit.Context,
				SourceType: string(domain.SourceTypeObservation),
				Source:     "semantic recall",
				Metadata:   metadata,
				Status:     string(domain.FragmentStatusActive),
			},
		})
	}
	return items
}
