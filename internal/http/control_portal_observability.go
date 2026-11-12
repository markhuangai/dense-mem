package http

import (
	"context"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
)

const (
	controlMetricsDefaultWindowMinutes = 60
	controlMetricsMaxWindowMinutes     = 43200
)

func controlMetricsFilter(c echo.Context) (domain.UsageMetricsFilter, error) {
	windowMinutes := controlMetricsDefaultWindowMinutes
	if raw := strings.TrimSpace(c.QueryParam("window_minutes")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > controlMetricsMaxWindowMinutes {
			return domain.UsageMetricsFilter{}, httperr.New(httperr.VALIDATION_ERROR, "window_minutes must be between 1 and 43200")
		}
		windowMinutes = parsed
	}

	var teamID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("team_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return domain.UsageMetricsFilter{}, httperr.New(httperr.INVALID_UUID, "invalid team ID format")
		}
		teamID = &parsed
	}
	var credentialID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("credential_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return domain.UsageMetricsFilter{}, httperr.New(httperr.INVALID_UUID, "invalid credential ID format")
		}
		credentialID = &parsed
	}

	to := time.Now().UTC()
	return domain.UsageMetricsFilter{
		From:         to.Add(-time.Duration(windowMinutes) * time.Minute),
		To:           to,
		TeamID:       teamID,
		CredentialID: credentialID,
	}, nil
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
	return response.PaginatedOK(c, page.Items, response.Pagination{
		Limit:  filter.Limit,
		Offset: filter.Offset,
		Total:  page.Total,
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
	return response.PaginatedOK(c, page.Items, response.Pagination{
		Limit:  filter.Limit,
		Offset: filter.Offset,
		Total:  page.Total,
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
	case "", "TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL":
	default:
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "severity must be one of TRACE, DEBUG, INFO, WARN, ERROR, FATAL")
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
	var teamID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("team_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "team ID must be a valid UUID")
		}
		teamID = &parsed
	}
	event := strings.TrimSpace(c.QueryParam("event"))
	if len(event) > 128 {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "event must be at most 128 characters")
	}
	correlationID := strings.TrimSpace(c.QueryParam("correlation_id"))
	if len(correlationID) > 128 {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "correlation_id must be at most 128 characters")
	}
	invocationID := strings.TrimSpace(c.QueryParam("invocation_id"))
	if len(invocationID) > 128 {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "invocation_id must be at most 128 characters")
	}
	requestHash := strings.TrimSpace(c.QueryParam("request_hash"))
	if len(requestHash) > 256 {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "request_hash must be at most 256 characters")
	}
	attemptID := strings.TrimSpace(c.QueryParam("attempt_id"))
	if len(attemptID) > 128 {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "attempt_id must be at most 128 characters")
	}
	classification := strings.TrimSpace(c.QueryParam("classification"))
	switch classification {
	case "", "execution", "replay", "conflict":
	default:
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "classification must be execution, replay, or conflict")
	}
	var retryable *bool
	if raw := strings.TrimSpace(c.QueryParam("retryable")); raw != "" {
		var err error
		retryable, err = optionalStrictControlBool(raw, "retryable")
		if err != nil {
			return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "retryable must be true or false")
		}
	}
	referenceType := strings.TrimSpace(c.QueryParam("reference_type"))
	if len(referenceType) > 64 {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "reference_type must be at most 64 characters")
	}
	referenceID := strings.TrimSpace(c.QueryParam("reference_id"))
	if len(referenceID) > 128 {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "reference_id must be at most 128 characters")
	}
	from, err := optionalControlTime(c.QueryParam("from"), "from")
	if err != nil {
		return domain.OperationLogFilter{}, err
	}
	to, err := optionalControlTime(c.QueryParam("to"), "to")
	if err != nil {
		return domain.OperationLogFilter{}, err
	}
	if from != nil && to != nil && from.After(*to) {
		return domain.OperationLogFilter{}, httperr.New(httperr.VALIDATION_ERROR, "from must be before or equal to to")
	}
	return domain.OperationLogFilter{
		Limit:          limit,
		Offset:         offset,
		Severity:       severity,
		Sort:           sort,
		Direction:      direction,
		Event:          event,
		CorrelationID:  correlationID,
		InvocationID:   invocationID,
		RequestHash:    requestHash,
		AttemptID:      attemptID,
		Classification: classification,
		Retryable:      retryable,
		TeamID:         teamID,
		ReferenceType:  referenceType,
		ReferenceID:    referenceID,
		From:           from,
		To:             to,
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

func optionalStrictControlBool(raw string, name string) (*bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var parsed bool
	switch raw {
	case "true":
		parsed = true
	case "false":
		parsed = false
	default:
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
	return observeDependencies(ctx, health).Dependencies
}

type controlDependencyObservation struct {
	CheckedAt    string
	Dependencies []controlDependencyResponse
}

func observeDependencies(ctx context.Context, health HealthConfig) controlDependencyObservation {
	results := runDependencyChecks(ctx, health.dependencyCheckRegistry(), health.Checks)
	responses := make([]controlDependencyResponse, 0, len(results)+1)
	for _, result := range results {
		if result.Check.Check == nil {
			continue
		}
		status := "ok"
		if result.Err != nil || result.TimedOut {
			status = "error"
			if result.Check.Optional {
				status = "degraded"
			}
		}
		message, reasonCode := dependencyFailureDisclosure(result)
		latency := result.Latency.Milliseconds()
		responses = append(responses, controlDependencyResponse{
			Name:       result.Check.Name,
			Status:     status,
			LatencyMS:  &latency,
			Message:    message,
			ReasonCode: reasonCode,
		})
	}
	if health.Degraded {
		message := "Single-node coordination is active."
		reasonCode := "single_node_mode"
		responses = append(responses, controlDependencyResponse{
			Name:       "redis",
			Status:     "degraded",
			Message:    &message,
			ReasonCode: &reasonCode,
		})
	}
	return controlDependencyObservation{
		CheckedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Dependencies: responses,
	}
}

func dependencyFailureDisclosure(result dependencyCheckResult) (*string, *string) {
	if result.Err == nil && !result.TimedOut {
		return nil, nil
	}
	reasonCode := "check_failed"
	message := "Dependency check failed."
	if result.TimedOut {
		reasonCode = "check_timeout"
		message = "Dependency check timed out."
	}
	return &message, &reasonCode
}
