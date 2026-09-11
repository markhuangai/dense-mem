package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/dream"
	dto "github.com/markhuangai/dense-mem/internal/http/dto"
	"github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/http/validation"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/recall"
)

// RecallHandlerInterface is the companion interface for recall handlers.
type RecallHandlerInterface interface {
	Handle(c echo.Context) error
}

// RecallHandler serves GET /ui/api/recall from the active recall pipeline.
type RecallHandler struct {
	svc    recall.RecallService
	dreams recallDreamingConfigProvider
}

type recallDreamingConfigProvider interface {
	EffectiveConfig(ctx context.Context, fallbackTeamID string) (dream.EffectiveConfig, error)
}

var _ RecallHandlerInterface = (*RecallHandler)(nil)

func NewRecallHandler(svc recall.RecallService, dreams recallDreamingConfigProvider) *RecallHandler {
	return &RecallHandler{svc: svc, dreams: dreams}
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

	dreamingEnabled := recallDreamingEnabled(ctx, h.dreams)
	result, err := h.svc.Recall(ctx, recall.RecallRequest{
		Query:                      req.Query,
		Limit:                      req.Limit,
		IncludeHypotheses:          dreamingEnabled,
		RelationshipLimit:          req.RelationshipLimit,
		CommunityLimit:             req.CommunityLimit,
		CommunityRelationshipLimit: req.CommunityRelationshipLimit,
		ValidAt:                    req.ValidAt,
		KnownAt:                    req.KnownAt,
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
		if errors.Is(err, recall.ErrRecallAuthContext) {
			return httperr.New(httperr.FORBIDDEN, "authentication required")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "recall failed")
	}
	if result != nil && !dreamingEnabled {
		result.RelatedHypotheses = []recall.RelatedHypothesisSummary{}
	}
	if degradation := requiredRecallDegradation(result); degradation != nil {
		message := strings.TrimSpace(degradation.Message)
		if message == "" {
			message = "recall unavailable"
		}
		return httperr.New(httperr.SERVICE_UNAVAILABLE, message)
	}

	return response.SuccessOK(c, recallHTTPResult(result))
}

func recallDreamingEnabled(ctx context.Context, cfg recallDreamingConfigProvider) bool {
	if cfg == nil {
		return false
	}
	effective, err := cfg.EffectiveConfig(ctx, "")
	return err == nil && effective.Enabled
}

func recallHTTPResult(result *recall.RecallResult) recall.RecallResult {
	if result == nil {
		result = &recall.RecallResult{}
	}
	out := *result
	if out.Results == nil {
		out.Results = []recall.RecallResultItem{}
	}
	if out.Conflicts == nil {
		out.Conflicts = []recall.RecallConflictSummary{}
	}
	if out.RelatedRelationships == nil {
		out.RelatedRelationships = []recall.RelatedRelationshipSummary{}
	}
	if out.RelatedCommunities == nil {
		out.RelatedCommunities = []recall.RecallDiscoveryPath{}
	}
	if out.DiscoveryPaths == nil {
		out.DiscoveryPaths = []recall.RecallDiscoveryPath{}
	}
	if out.RelatedHypotheses == nil {
		out.RelatedHypotheses = []recall.RelatedHypothesisSummary{}
	}
	if out.Degradations == nil {
		out.Degradations = []recall.RecallDegradationResult{}
	}
	if out.SearchStates.Evidence == "" {
		out.SearchStates.Evidence = string(domain.SearchProjectionCurrent)
	}
	if out.SearchStates.Relationships == "" {
		out.SearchStates.Relationships = string(domain.SearchProjectionCurrent)
	}
	if strings.TrimSpace(out.DiscoveryGuidance) == "" {
		out.DiscoveryGuidance = "No additional discovery guidance."
	}
	return out
}

func requiredRecallDegradation(result *recall.RecallResult) *recall.RecallDegradationResult {
	if result == nil {
		return nil
	}
	for i := range result.Degradations {
		if result.Degradations[i].RequiredFailure {
			return &result.Degradations[i]
		}
	}
	if result.Degradation != nil && result.Degradation.RequiredFailure {
		return result.Degradation
	}
	return nil
}
