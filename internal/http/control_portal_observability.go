package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
)

type controlDreamListResponse struct {
	Items      []*domain.Dream `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func (h *controlPortalHandler) listOperationLogs(c echo.Context) error {
	if h.operationLogs == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "operation logs unavailable")
	}
	filter, err := controlOperationLogsFilter(c)
	if err != nil {
		return err
	}
	page, err := h.operationLogs.ListOperationLogs(c.Request().Context(), filter)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, handler.PaginationEnvelope{
		Data: page.Items,
		Pagination: handler.Pagination{
			Limit:  filter.Limit,
			Offset: filter.Offset,
			Total:  page.Total,
		},
	})
}

func (h *controlPortalHandler) listRecallFeedbackEvents(c echo.Context) error {
	if h.recallFeedback == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "recall feedback events unavailable")
	}
	filter, err := controlRecallFeedbackEventsFilter(c)
	if err != nil {
		return err
	}
	page, err := h.recallFeedback.ListRecallFeedbackEvents(c.Request().Context(), filter)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, handler.PaginationEnvelope{
		Data: page.Items,
		Pagination: handler.Pagination{
			Limit:  filter.Limit,
			Offset: filter.Offset,
			Total:  page.Total,
		},
	})
}

func (h *controlPortalHandler) getRecallFeedbackEvent(c echo.Context) error {
	if h.recallFeedback == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "recall feedback events unavailable")
	}
	recallID := strings.TrimSpace(c.Param("recallId"))
	if recallID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "recall ID is required")
	}
	event, err := h.recallFeedback.GetRecallFeedbackEvent(c.Request().Context(), recallID)
	if err != nil {
		return err
	}
	if event == nil {
		return httperr.New(httperr.NOT_FOUND, "recall feedback event not found")
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": event})
}

func (h *controlPortalHandler) getTeamDreamingStatus(c echo.Context) error {
	if h.dreams == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "dream service unavailable")
	}
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	status, err := h.dreams.Status(c.Request().Context(), profileID.String())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": status})
}

func (h *controlPortalHandler) listTeamDreamingRuns(c echo.Context) error {
	if h.dreams == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "dream service unavailable")
	}
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	limit, err := controlDreamLimit(c.QueryParam("limit"))
	if err != nil {
		return err
	}
	runs, err := h.dreams.ListRuns(c.Request().Context(), profileID.String(), limit)
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": runs})
}

func (h *controlPortalHandler) listTeamDreams(c echo.Context) error {
	if h.dreams == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "dream service unavailable")
	}
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	opts, err := controlDreamListOptions(c)
	if err != nil {
		return err
	}
	dreams, nextCursor, err := h.dreams.List(c.Request().Context(), profileID.String(), opts)
	if err != nil {
		if errors.Is(err, dreamservice.ErrInvalidDreamCursor) {
			return httperr.New(httperr.VALIDATION_ERROR, "invalid cursor")
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": controlDreamListResponse{Items: dreams, NextCursor: nextCursor}})
}

func (h *controlPortalHandler) getTeamDream(c echo.Context) error {
	if h.dreams == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "dream service unavailable")
	}
	profileID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	dreamID := strings.TrimSpace(c.Param("dreamId"))
	if dreamID == "" {
		return httperr.New(httperr.VALIDATION_ERROR, "dream ID is required")
	}
	dream, err := h.dreams.Get(c.Request().Context(), profileID.String(), dreamID)
	if err != nil {
		if errors.Is(err, dreamservice.ErrDreamNotFound) {
			return httperr.New(httperr.NOT_FOUND, "dream not found")
		}
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": dream})
}

func controlOperationLogsFilter(c echo.Context) (domain.OperationLogFilter, error) {
	limit, offset := controlPagination(c)
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 500")
		}
		limit = parsed
	}
	severity := strings.ToUpper(strings.TrimSpace(c.QueryParam("severity")))
	switch severity {
	case "", "DEBUG", "INFO", "WARN", "ERROR":
	default:
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "severity must be one of DEBUG, INFO, WARN, ERROR")
	}
	sort := strings.ToLower(strings.TrimSpace(c.QueryParam("sort")))
	switch sort {
	case "", "timestamp", "severity":
	default:
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "sort must be timestamp or severity")
	}
	direction := strings.ToLower(strings.TrimSpace(c.QueryParam("direction")))
	switch direction {
	case "", "asc", "desc":
	default:
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "direction must be asc or desc")
	}
	return domain.OperationLogFilter{
		Limit:     limit,
		Offset:    offset,
		Severity:  severity,
		Sort:      sort,
		Direction: direction,
	}, nil
}

func controlRecallFeedbackEventsFilter(c echo.Context) (domain.RecallFeedbackEventFilter, error) {
	limit, offset := controlPagination(c)
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			return domain.RecallFeedbackEventFilter{}, httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 500")
		}
		limit = parsed
	}
	filter := domain.RecallFeedbackEventFilter{Limit: limit, Offset: offset}
	if raw := strings.TrimSpace(c.QueryParam("team_id")); raw != "" {
		id, err := parseControlUUID(raw, "team ID")
		if err != nil {
			return domain.RecallFeedbackEventFilter{}, err
		}
		filter.TeamID = &id
	}
	if raw := strings.TrimSpace(c.QueryParam("profile_id")); raw != "" {
		id, err := parseControlUUID(raw, "profile ID")
		if err != nil {
			return domain.RecallFeedbackEventFilter{}, err
		}
		filter.ProfileID = &id
	}
	quality := strings.ToLower(strings.TrimSpace(c.QueryParam("quality")))
	switch quality {
	case "", "high", "medium", "low":
		filter.Quality = quality
	default:
		return domain.RecallFeedbackEventFilter{}, httperr.New(httperr.VALIDATION_ERROR, "quality must be one of high, medium, low")
	}
	includePending, err := optionalControlBool(c.QueryParam("include_pending"), "include_pending")
	if err != nil {
		return domain.RecallFeedbackEventFilter{}, err
	}
	if includePending != nil {
		filter.IncludePending = *includePending
	}
	missingContext, err := optionalControlBool(c.QueryParam("missing_context"), "missing_context")
	if err != nil {
		return domain.RecallFeedbackEventFilter{}, err
	}
	filter.MissingContext = missingContext
	irrelevant, err := optionalControlBool(c.QueryParam("irrelevant"), "irrelevant")
	if err != nil {
		return domain.RecallFeedbackEventFilter{}, err
	}
	filter.Irrelevant = irrelevant
	from, err := optionalControlTime(c.QueryParam("from"), "from")
	if err != nil {
		return domain.RecallFeedbackEventFilter{}, err
	}
	filter.From = from
	to, err := optionalControlTime(c.QueryParam("to"), "to")
	if err != nil {
		return domain.RecallFeedbackEventFilter{}, err
	}
	filter.To = to
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return domain.RecallFeedbackEventFilter{}, httperr.New(httperr.VALIDATION_ERROR, "from must be before or equal to to")
	}
	return filter, nil
}

func controlDreamListOptions(c echo.Context) (dreamservice.ListOptions, error) {
	limit, err := controlDreamLimit(c.QueryParam("limit"))
	if err != nil {
		return dreamservice.ListOptions{}, err
	}
	status := strings.TrimSpace(c.QueryParam("status"))
	if status != "" && !domain.DreamStatus(status).IsValid() {
		return dreamservice.ListOptions{}, httperr.New(httperr.VALIDATION_ERROR, "status must be one of proposed, reinforced, stale, rejected, promoted")
	}
	sort := strings.TrimSpace(c.QueryParam("sort"))
	switch sort {
	case "", dreamservice.DreamSortUpdatedAt, dreamservice.DreamSortCreatedAt, dreamservice.DreamSortLastEvaluatedAt:
	default:
		return dreamservice.ListOptions{}, httperr.New(httperr.VALIDATION_ERROR, "sort must be updated_at, created_at, or last_evaluated_at")
	}
	direction := strings.TrimSpace(c.QueryParam("direction"))
	switch direction {
	case "", dreamservice.DreamDirectionAsc, dreamservice.DreamDirectionDesc:
	default:
		return dreamservice.ListOptions{}, httperr.New(httperr.VALIDATION_ERROR, "direction must be asc or desc")
	}
	return dreamservice.ListOptions{
		Limit:     limit,
		Status:    status,
		Cursor:    strings.TrimSpace(c.QueryParam("cursor")),
		Sort:      sort,
		Direction: direction,
	}, nil
}

func controlDreamLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 20, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 || parsed > 100 {
		return 0, httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
	}
	return parsed, nil
}

func optionalControlBool(raw string, name string) (*bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, httperr.New(httperr.VALIDATION_ERROR, name+" must be true or false")
	}
	return &parsed, nil
}

func optionalControlTime(raw string, name string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, httperr.New(httperr.VALIDATION_ERROR, name+" must be RFC3339")
	}
	return &parsed, nil
}

func controlDependencySnapshot(ctx context.Context, health HealthConfig) []controlDependencyResponse {
	responses := make([]controlDependencyResponse, 0, len(health.Checks)+1)
	for _, check := range health.Checks {
		if check.Check == nil {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		start := time.Now()
		err := check.Check(checkCtx)
		latency := time.Since(start).Milliseconds()
		cancel()

		status := "ok"
		var message *string
		if err != nil {
			status = "error"
			if check.Optional {
				status = "degraded"
			}
			text := err.Error()
			message = &text
		}
		responses = append(responses, controlDependencyResponse{
			Name:      check.Name,
			Status:    status,
			LatencyMS: &latency,
			Message:   message,
		})
	}
	if health.Degraded {
		message := health.Reason
		if message == "" {
			message = "degraded mode"
		}
		responses = append(responses, controlDependencyResponse{
			Name:    "redis",
			Status:  "degraded",
			Message: &message,
		})
	}
	return responses
}
