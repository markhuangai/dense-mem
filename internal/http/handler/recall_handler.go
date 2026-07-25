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
	_, ok := middleware.GetResolvedTeamID(ctx)
	if !ok {
		return httperr.New(httperr.PROFILE_ID_REQUIRED, "team ID is required")
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
		KnownEvidenceIDs: append(
			[]string(nil),
			req.KnownEvidenceIDs...,
		),
		KnownRelationshipIDs: append(
			[]string(nil),
			req.KnownRelationshipIDs...,
		),
		ExpandFromEntityIDs: append(
			[]string(nil),
			req.ExpandFromEntityIDs...,
		),
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

	return response.SuccessOK(c, recallHTTPResult(result))
}

func recallHTTPResult(result *memoryservice.V2RecallResult) memoryservice.V2RecallResult {
	if result == nil {
		result = &memoryservice.V2RecallResult{}
	}
	out := *result
	if out.Results == nil {
		out.Results = []memoryservice.V2RecallResultItem{}
	}
	if out.Conflicts == nil {
		out.Conflicts = []memoryservice.V2RecallConflictSummary{}
	}
	if out.DiscoveryPaths == nil {
		out.DiscoveryPaths = []memoryservice.V2RecallDiscoveryPath{}
	}
	if out.RelatedHypotheses == nil {
		out.RelatedHypotheses = []memoryservice.V2RelatedHypothesisSummary{}
	}
	if strings.TrimSpace(out.DiscoveryGuidance) == "" {
		out.DiscoveryGuidance = "No additional discovery guidance."
	}
	return out
}
