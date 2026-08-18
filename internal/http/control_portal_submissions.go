package http

import (
	"errors"
	nethttp "net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/handler"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (h *controlPortalHandler) listSubmissionDiagnostics(c echo.Context) error {
	if h.submissions == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "submission diagnostics unavailable")
	}
	filter, err := controlSubmissionDiagnosticFilter(c)
	if err != nil {
		return err
	}
	page, err := h.submissions.ListSubmissionDiagnostics(c.Request().Context(), filter)
	if errors.Is(err, service.ErrSubmissionDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "submission diagnostics unavailable")
	}
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, handler.PaginationEnvelope{
		Data:       page.Items,
		Pagination: handler.Pagination{Limit: filter.Limit, Offset: filter.Offset, Total: page.Total},
	})
}

func (h *controlPortalHandler) getSubmissionDiagnostic(c echo.Context) error {
	if h.submissions == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "submission diagnostics unavailable")
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
		return httperr.New(httperr.NOT_FOUND, "submission not found")
	}
	if errors.Is(err, service.ErrSubmissionDiagnosticsUnavailable) {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "submission diagnostics unavailable")
	}
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": detail})
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
	case "", "queued", "processing", "awaiting_review", "completed", "rejected", "quarantined", "failed":
	default:
		return service.SubmissionDiagnosticFilter{}, httperr.New(httperr.VALIDATION_ERROR, "processing_state is unsupported")
	}
	return service.SubmissionDiagnosticFilter{TeamID: teamID, ProcessingState: state, Limit: limit, Offset: offset}, nil
}
