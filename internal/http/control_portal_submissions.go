package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (h *controlPortalHandler) listSubmissionDiagnostics(c echo.Context) error {
	if h.submissions == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempts unavailable")
	}
	filter, err := controlSubmissionDiagnosticFilter(c)
	if err != nil {
		return err
	}
	page, err := h.submissions.ListSubmissionDiagnostics(c.Request().Context(), filter)
	if errors.Is(err, service.ErrSubmissionDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempts unavailable")
	}
	if err != nil {
		return err
	}
	return response.PaginatedOK(c, page.Items, response.Pagination{
		Limit: filter.Limit, Offset: filter.Offset, Total: page.Total,
	})
}

func (h *controlPortalHandler) getSubmissionDiagnostic(c echo.Context) error {
	if h.submissions == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempts unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	submissionID, err := parseControlUUID(c.Param("submissionId"), "submission ID")
	if err != nil {
		return err
	}
	detail, err := h.submissions.GetSubmissionDiagnostic(c.Request().Context(), teamID.String(), submissionID.String())
	if errors.Is(err, service.ErrSubmissionDiagnosticNotFound) {
		return httperr.New(httperr.NOT_FOUND, "remember attempt not found")
	}
	if errors.Is(err, service.ErrSubmissionDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember attempts unavailable")
	}
	if err != nil {
		return err
	}
	return response.SuccessOK(c, detail)
}

func (h *controlPortalHandler) getRememberFailureArtifact(c echo.Context) error {
	reader, ok := h.submissions.(interface {
		GetRememberFailureArtifact(context.Context, string, string, string) (*repository.RememberFailureArtifact, error)
	})
	if !ok {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember failure artifacts unavailable")
	}
	teamID, err := parseControlUUID(controlTeamIDParam(c), "team ID")
	if err != nil {
		return err
	}
	attemptID, err := parseControlUUID(c.Param("submissionId"), "submission ID")
	if err != nil {
		return err
	}
	artifactID, err := parseControlUUID(c.Param("artifactId"), "artifact ID")
	if err != nil {
		return err
	}
	artifact, err := reader.GetRememberFailureArtifact(c.Request().Context(), teamID.String(), attemptID.String(), artifactID.String())
	if errors.Is(err, service.ErrSubmissionDiagnosticNotFound) {
		return httperr.New(httperr.NOT_FOUND, "remember failure artifact not found or expired")
	}
	if errors.Is(err, service.ErrSubmissionDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "remember failure artifacts unavailable")
	}
	if err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, artifact.ContentType, artifact.Content)
}

func controlSubmissionDiagnosticFilter(c echo.Context) (service.SubmissionDiagnosticFilter, error) {
	limit, offset := controlPagination(c)
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return service.SubmissionDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "limit must be between 1 and 100")
		}
		limit = parsed
	}
	teamID := strings.TrimSpace(c.QueryParam("team_id"))
	if teamID != "" {
		parsed, err := uuid.Parse(teamID)
		if err != nil {
			return service.SubmissionDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "team ID must be a valid UUID")
		}
		teamID = parsed.String()
	}
	state := strings.TrimSpace(c.QueryParam("processing_state"))
	switch state {
	case "", "completed", "rejected", "quarantined", "failed", "replayed":
	default:
		return service.SubmissionDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "processing_state is unsupported")
	}
	return service.SubmissionDiagnosticFilter{TeamID: teamID, ProcessingState: state, Limit: limit, Offset: offset}, nil
}
