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
	"github.com/markhuangai/dense-mem/internal/recall"
)

// RecallHandler serves GET /ui/api/recall from the active recall pipeline.
type RecallHandler struct {
	svc recall.RecallService
}

func NewRecallHandler(svc recall.RecallService) *RecallHandler {
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

	for key := range c.QueryParams() {
		switch key {
		case "query", "limit", "topic_ids", "topic_limit", "valid_at", "known_at", "known_evidence_ids":
		default:
			return httperr.New(httperr.VALIDATION_ERROR, "unknown recall query parameter")
		}
	}
	var req dto.RecallRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid query parameters")
	}
	if err := validation.ValidateStruct(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if strings.TrimSpace(req.Query) == "" && len(req.TopicIDs) == 0 {
		return httperr.New(httperr.VALIDATION_ERROR, "query or topic_ids is required")
	}
	result, err := h.svc.Recall(ctx, recall.RecallRequest{
		Query: req.Query, Limit: req.Limit, TopicIDs: append([]string(nil), req.TopicIDs...), TopicLimit: req.TopicLimit,
		ValidAt: req.ValidAt, KnownAt: req.KnownAt, KnownEvidenceIDs: append([]string{}, req.KnownEvidenceIDs...),
	})
	if err != nil {
		if errors.Is(err, recall.ErrInvalidInput) {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid recall query parameters")
		}
		if errors.Is(err, recall.ErrTopicUnavailable) {
			return httperr.WithGuidance(httperr.New(httperr.VALIDATION_ERROR, "A requested topic is unavailable."), "topic_unavailable", "refresh_state", "Recall again to obtain current authorized topic IDs.", false, nil, "")
		}
		if errors.Is(err, recall.ErrRecallAuthContext) {
			return httperr.New(httperr.FORBIDDEN, "authentication required")
		}
		return httperr.New(httperr.INTERNAL_ERROR, "recall failed")
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
	if out.RelatedTopics == nil {
		out.RelatedTopics = []recall.RelatedTopic{}
	}
	if out.Degradations == nil {
		out.Degradations = []recall.RecallDegradationResult{}
	}
	if out.SearchStates.Evidence == "" {
		out.SearchStates.Evidence = string(domain.SearchProjectionCurrent)
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
