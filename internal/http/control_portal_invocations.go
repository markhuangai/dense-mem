package http

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/domain"
	httpcontract "github.com/markhuangai/dense-mem/internal/http/contract"
	"github.com/markhuangai/dense-mem/internal/http/handler"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func registerRememberInvocationDiagnosticsRoutes(api *echo.Group, control *controlPortalHandler, telemetry ControlPortalTelemetry) {
	if telemetry.RememberInvocations == nil {
		return
	}
	api.GET("/remember-invocations", control.listRememberInvocationDiagnostics)
	api.GET("/teams/:teamId/remember-invocations/:invocationId", control.getRememberInvocationDiagnostic)
}

func (h *controlPortalHandler) listRememberInvocationDiagnostics(c echo.Context) error {
	if h.rememberInvocations == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember invocation diagnostics unavailable")
	}
	filter, err := controlRememberInvocationDiagnosticFilter(c)
	if err != nil {
		return err
	}
	page, err := h.rememberInvocations.ListRememberInvocationDiagnostics(c.Request().Context(), filter)
	if errors.Is(err, rememberapp.ErrRememberInvocationDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember invocation diagnostics unavailable")
	}
	if err != nil {
		return err
	}
	return c.JSON(200, handler.PaginationEnvelope{
		Data:       page.Items,
		Pagination: handler.Pagination{Limit: filter.Limit, Offset: filter.Offset, Total: page.Total},
	})
}

func (h *controlPortalHandler) getRememberInvocationDiagnostic(c echo.Context) error {
	if h.rememberInvocations == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember invocation diagnostics unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	invocationID, err := parseControlUUID(c.Param("invocationId"), "invocation ID")
	if err != nil {
		return err
	}
	detail, err := h.rememberInvocations.GetRememberInvocationDiagnostic(c.Request().Context(), teamID.String(), invocationID.String())
	if errors.Is(err, rememberapp.ErrRememberInvocationDiagnosticNotFound) {
		return httperr.New(httperr.NOT_FOUND, "remember invocation not found")
	}
	if errors.Is(err, rememberapp.ErrRememberInvocationDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember invocation diagnostics unavailable")
	}
	if err != nil {
		return err
	}
	h.enrichRememberInvocationDiagnostic(c.Request().Context(), teamID, detail)
	if h.logger != nil {
		h.logger.Info("control_remember_invocation_diagnostic_access",
			httpcontract.String("actor", controlPortalActorFromContext(c.Request().Context())),
			httpcontract.String("actor_identity_id", controlPortalActorIdentityFromContext(c.Request().Context())),
			httpcontract.String("team_id", teamID.String()),
			httpcontract.String("invocation_id", invocationID.String()),
			httpcontract.String("correlation_id", httpmw.GetCorrelationID(c.Request().Context())),
		)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.JSON(200, map[string]any{"data": detail})
}

func (h *controlPortalHandler) enrichRememberInvocationDiagnostic(ctx context.Context, teamID uuid.UUID, detail *rememberapp.RememberInvocationDiagnosticDetail) {
	if detail == nil {
		return
	}
	if h == nil || h.operationLogs == nil {
		detail.EnrichmentUnavailable = true
		return
	}
	enrichmentUnavailable := false
	var transportLogs []domain.OperationLog
	read := func(filter domain.OperationLogFilter) []domain.OperationLog {
		filter.Limit = 100
		page, err := h.operationLogs.ListOperationLogs(ctx, filter)
		if err != nil || page == nil {
			enrichmentUnavailable = true
			return nil
		}
		return page.Items
	}
	invocationLogs := read(domain.OperationLogFilter{TeamID: &teamID, InvocationID: detail.InvocationID})
	enrichRememberInvocationFromInvocationLogs(detail, invocationLogs)
	completionObserved := rememberInvocationCompletionObserved(detail.InvocationID, invocationLogs)
	transportObserved := enrichRememberInvocationFromTransportLogs(detail, invocationLogs)
	if (detail.DeliveryStage == "" || detail.DeliveryStage == "unknown_receipt") && strings.TrimSpace(detail.CorrelationID) != "" {
		transportLogs = read(domain.OperationLogFilter{TeamID: &teamID, CorrelationID: detail.CorrelationID})
		transportObserved = enrichRememberInvocationFromTransportLogs(detail, transportLogs) || transportObserved
	}
	if !completionObserved || (strings.TrimSpace(detail.CorrelationID) != "" && !transportObserved) {
		enrichmentUnavailable = true
	}
	detail.EnrichmentUnavailable = enrichmentUnavailable
}

func rememberInvocationCompletionObserved(invocationID string, logs []domain.OperationLog) bool {
	for _, log := range logs {
		if log.Message != "remember_invocation_completed" {
			continue
		}
		if loggedInvocationID, ok := log.Attrs["invocation_id"].(string); ok && strings.TrimSpace(loggedInvocationID) == invocationID {
			return true
		}
	}
	return false
}

func enrichRememberInvocationFromInvocationLogs(detail *rememberapp.RememberInvocationDiagnosticDetail, logs []domain.OperationLog) {
	phaseSet := false
	for _, log := range logs {
		if !phaseSet {
			if phase, ok := log.Attrs["phase"].(string); ok && strings.TrimSpace(phase) != "" {
				detail.Phase = phase
				phaseSet = true
			}
		}
		if strings.TrimSpace(log.Error) != "" && strings.TrimSpace(detail.ProtectedCause) == "" {
			detail.ProtectedCause = log.Error
		}
		if phaseSet && strings.TrimSpace(detail.ProtectedCause) != "" {
			break
		}
	}
}

func enrichRememberInvocationFromTransportLogs(detail *rememberapp.RememberInvocationDiagnosticDetail, logs []domain.OperationLog) bool {
	for _, log := range logs {
		if log.Message != "http_request" && log.Message != "control_http_request" {
			continue
		}
		stage, ok := log.Attrs["delivery_stage"].(string)
		if !ok || strings.TrimSpace(stage) == "" {
			continue
		}
		if invocationID, ok := log.Attrs["invocation_id"].(string); ok && strings.TrimSpace(invocationID) != "" {
			if invocationID == detail.InvocationID {
				detail.DeliveryStage = stage
				return true
			}
		}
	}
	return false
}

func controlRememberInvocationDiagnosticFilter(c echo.Context) (rememberapp.RememberInvocationDiagnosticFilter, error) {
	limit, offset := controlPagination(c)
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return rememberapp.RememberInvocationDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
		}
		limit = parsed
	}
	if raw := strings.TrimSpace(c.QueryParam("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return rememberapp.RememberInvocationDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "offset must be a non-negative integer")
		}
		offset = parsed
	}
	filter := rememberapp.RememberInvocationDiagnosticFilter{
		TeamID:             strings.TrimSpace(c.QueryParam("team_id")),
		OwnerProfileID:     strings.TrimSpace(c.QueryParam("owner_profile_id")),
		InvocationID:       strings.TrimSpace(c.QueryParam("invocation_id")),
		CanonicalAttemptID: strings.TrimSpace(c.QueryParam("canonical_attempt_id")),
		RequestHash:        strings.TrimSpace(c.QueryParam("request_hash")),
		CorrelationID:      strings.TrimSpace(c.QueryParam("correlation_id")),
		Classification:     strings.TrimSpace(c.QueryParam("classification")),
		Outcome:            strings.TrimSpace(c.QueryParam("outcome")),
		Limit:              limit, Offset: offset,
	}
	for name, value := range map[string]string{
		"team_id": filter.TeamID, "owner_profile_id": filter.OwnerProfileID,
		"invocation_id": filter.InvocationID, "canonical_attempt_id": filter.CanonicalAttemptID,
	} {
		if value != "" {
			if _, err := uuid.Parse(value); err != nil {
				return rememberapp.RememberInvocationDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, name+" must be a valid UUID")
			}
		}
	}
	if len(filter.RequestHash) > 256 || len(filter.CorrelationID) > 128 {
		return rememberapp.RememberInvocationDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "diagnostic identity filter is too long")
	}
	switch filter.Classification {
	case "", "execution", "replay", "conflict":
	default:
		return rememberapp.RememberInvocationDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "classification is unsupported")
	}
	switch filter.Outcome {
	case "", "completed", "evaluated_zero", "failed", "cancelled", "replayed", "conflict":
	default:
		return rememberapp.RememberInvocationDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "outcome is unsupported")
	}
	if raw := strings.TrimSpace(c.QueryParam("retryable")); raw != "" {
		value, err := optionalStrictControlBool(raw, "retryable")
		if err != nil {
			return rememberapp.RememberInvocationDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "retryable must be true or false")
		}
		filter.Retryable = value
	}
	return filter, nil
}
