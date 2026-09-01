package http

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (h *controlPortalHandler) listRememberAttemptDiagnostics(c echo.Context) error {
	if h.rememberAttempts == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempt diagnostics unavailable")
	}
	filter, err := controlRememberAttemptDiagnosticFilter(c)
	if err != nil {
		return err
	}
	page, err := h.rememberAttempts.ListRememberAttemptDiagnostics(c.Request().Context(), filter)
	if errors.Is(err, service.ErrRememberAttemptDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempt diagnostics unavailable")
	}
	if err != nil {
		return err
	}
	return response.PaginatedOK(c, page.Items, response.Pagination{Limit: filter.Limit, Offset: filter.Offset, Total: page.Total})
}

func (h *controlPortalHandler) getRememberAttemptDiagnostic(c echo.Context) error {
	if h.rememberAttempts == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempt diagnostics unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	attemptID, err := parseControlUUID(c.Param("attemptId"), "attempt ID")
	if err != nil {
		return err
	}
	detail, err := h.rememberAttempts.GetRememberAttemptDiagnostic(c.Request().Context(), teamID.String(), attemptID.String())
	if errors.Is(err, service.ErrRememberAttemptDiagnosticNotFound) {
		return httperr.New(httperr.NOT_FOUND, "remember attempt not found")
	}
	if errors.Is(err, service.ErrRememberAttemptDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempt diagnostics unavailable")
	}
	if err != nil {
		return err
	}
	return response.SuccessOK(c, detail)
}

func (h *controlPortalHandler) getRememberFailureArtifact(c echo.Context) error {
	if h.rememberAttempts == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempt diagnostics unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	attemptID, err := parseControlUUID(c.Param("attemptId"), "attempt ID")
	if err != nil {
		return err
	}
	artifactID, err := parseControlUUID(c.Param("artifactId"), "artifact ID")
	if err != nil {
		return err
	}
	artifact, err := h.rememberAttempts.GetRememberFailureArtifact(c.Request().Context(), teamID.String(), attemptID.String(), artifactID.String())
	if errors.Is(err, service.ErrRememberFailureArtifactNotFound) {
		return httperr.New(httperr.NOT_FOUND, "remember failure artifact not found")
	}
	if errors.Is(err, service.ErrRememberAttemptDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempt diagnostics unavailable")
	}
	if err != nil {
		return err
	}
	if h.logger != nil {
		h.logger.Info("control_remember_failure_artifact_access",
			observability.String("actor", controlPortalActorFromContext(c.Request().Context())),
			observability.String("actor_identity_id", controlPortalActorIdentityFromContext(c.Request().Context())),
			observability.String("team_id", teamID.String()),
			observability.String("attempt_id", attemptID.String()),
			observability.String("artifact_id", artifactID.String()),
			observability.String("correlation_id", httpmw.GetCorrelationID(c.Request().Context())),
		)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"remember-failure-%s\"", artifact.ArtifactID))
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.Blob(200, artifact.ContentType, artifact.Content)
}

func controlRememberAttemptDiagnosticFilter(c echo.Context) (service.RememberAttemptDiagnosticFilter, error) {
	limit, offset := controlPagination(c)
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return service.RememberAttemptDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
		}
		limit = parsed
	}
	if raw := strings.TrimSpace(c.QueryParam("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return service.RememberAttemptDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "offset must be a non-negative integer")
		}
		offset = parsed
	}
	teamID := strings.TrimSpace(c.QueryParam("team_id"))
	if teamID != "" {
		parsed, err := uuid.Parse(teamID)
		if err != nil {
			return service.RememberAttemptDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "team ID must be a valid UUID")
		}
		teamID = parsed.String()
	}
	outcome := strings.TrimSpace(c.QueryParam("outcome"))
	switch outcome {
	case "", "completed", "failed":
	default:
		return service.RememberAttemptDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "outcome is unsupported")
	}
	return service.RememberAttemptDiagnosticFilter{TeamID: teamID, Outcome: outcome, Limit: limit, Offset: offset}, nil
}
