package http

import (
	"errors"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

func (h *controlPortalHandler) getSearchConvergence(c echo.Context) error {
	if h.convergence == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "search convergence unavailable")
	}
	projection, err := h.convergence.GetSearchConvergence(c.Request().Context())
	if err != nil {
		if errors.Is(err, service.ErrSearchConvergenceUnavailable) {
			return httperr.New(httperr.SERVICE_UNAVAILABLE, "search convergence unavailable")
		}
		if h.logger != nil {
			h.logger.Warn("control_search_convergence_failed", observability.String("error_code", "search_convergence_query_failed"))
		}
		return httperr.New(httperr.INTERNAL_ERROR, "failed to load search convergence")
	}
	return response.SuccessOK(c, toControlSearchConvergence(projection))
}

type controlSearchConvergenceResponse struct {
	ObservedAt        string                         `json:"observed_at"`
	Status            string                         `json:"status"`
	Contract          *controlSearchContractResponse `json:"contract,omitempty"`
	ExpectedDocuments int64                          `json:"expected_documents"`
	CurrentDocuments  int64                          `json:"current_documents"`
	DriftedDocuments  int64                          `json:"drifted_documents"`
	AffectedTeamCount int64                          `json:"affected_team_count"`
	OldestDriftAge    float64                        `json:"oldest_drift_age_seconds"`
	DriftClasses      []controlSearchDriftResponse   `json:"drift_classes"`
	LatestRun         *controlSearchRunResponse      `json:"latest_run,omitempty"`
}

type controlSearchContractResponse struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Dimensions      int    `json:"dimensions"`
	IndexGeneration int    `json:"index_generation"`
	IndexStrategy   string `json:"index_strategy"`
}

type controlSearchDriftResponse struct {
	Class string `json:"class"`
	Count int64  `json:"count"`
}

type controlSearchRunResponse struct {
	RunID         string `json:"run_id"`
	LocalRunDate  string `json:"local_run_date"`
	Status        string `json:"status"`
	SelectedCount int64  `json:"selected_count"`
	EmbeddedCount int64  `json:"embedded_count"`
	UpdatedCount  int64  `json:"updated_count"`
	DriftedCount  int64  `json:"drifted_count"`
	LastError     string `json:"last_error,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

func toControlSearchConvergence(value *repository.SearchConvergence) controlSearchConvergenceResponse {
	result := controlSearchConvergenceResponse{DriftClasses: []controlSearchDriftResponse{}}
	if value == nil {
		return result
	}
	result.ObservedAt = value.ObservedAt.UTC().Format(time.RFC3339)
	result.Status = value.Status
	result.ExpectedDocuments = value.ExpectedDocuments
	result.CurrentDocuments = value.CurrentDocuments
	result.DriftedDocuments = value.DriftedDocuments
	result.AffectedTeamCount = value.AffectedTeamCount
	result.OldestDriftAge = value.OldestDriftAge.Seconds()
	if value.Contract != nil {
		result.Contract = &controlSearchContractResponse{
			Provider:        value.Contract.EmbeddingProvider,
			Model:           value.Contract.EmbeddingModel,
			Dimensions:      value.Contract.EmbeddingDimensions,
			IndexGeneration: value.Contract.IndexGeneration,
			IndexStrategy:   value.Contract.IndexStrategy,
		}
	}
	for _, drift := range value.DriftClasses {
		result.DriftClasses = append(result.DriftClasses, controlSearchDriftResponse{Class: drift.Class, Count: drift.Count})
	}
	if run := value.LatestRun; run != nil {
		result.LatestRun = &controlSearchRunResponse{
			RunID:         run.RunID,
			LocalRunDate:  run.LocalRunDate.Format("2006-01-02"),
			Status:        run.Status,
			SelectedCount: run.SelectedCount,
			EmbeddedCount: run.EmbeddedCount,
			UpdatedCount:  run.UpdatedCount,
			DriftedCount:  run.DriftedCount,
			LastError:     boundedControlSearchRunError(run.LastError),
			UpdatedAt:     run.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if run.StartedAt != nil {
			result.LatestRun.StartedAt = run.StartedAt.UTC().Format(time.RFC3339)
		}
		if run.CompletedAt != nil {
			result.LatestRun.CompletedAt = run.CompletedAt.UTC().Format(time.RFC3339)
		}
	}
	return result
}

func boundedControlSearchRunError(message string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if message == "" {
		return ""
	}
	const limit = 128
	if len(message) > limit {
		return "reconciliation operation failed"
	}
	if strings.Contains(message, "provider") || strings.Contains(message, "database") || strings.Contains(message, "pq:") {
		return "reconciliation operation failed"
	}
	return message
}
