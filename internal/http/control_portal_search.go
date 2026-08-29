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
	ObservedAt             string                              `json:"observed_at"`
	Status                 string                              `json:"status"`
	Contract               *controlSearchContractResponse      `json:"contract,omitempty"`
	ExpectedDocuments      int64                               `json:"expected_documents"`
	CurrentDocuments       int64                               `json:"current_documents"`
	DriftedDocuments       int64                               `json:"drifted_documents"`
	AffectedTeamCount      int64                               `json:"affected_team_count"`
	OldestDriftAge         float64                             `json:"oldest_drift_age_seconds"`
	DriftClasses           []controlSearchDriftClassResponse   `json:"drift_classes"`
	Queue                  controlSearchQueueResponse          `json:"queue"`
	Failures               []controlSearchFailureResponse      `json:"failures"`
	FailureGroups          []controlSearchFailureGroupResponse `json:"failure_groups"`
	FailureGroupCount      int64                               `json:"failure_group_count"`
	FailureGroupsTruncated bool                                `json:"failure_groups_truncated"`
	LatestRun              *controlSearchRunResponse           `json:"latest_run,omitempty"`
}

type controlSearchDriftClassResponse struct {
	Class string `json:"class"`
	Count int64  `json:"count"`
}

type controlSearchContractResponse struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Dimensions      int    `json:"dimensions"`
	IndexGeneration int    `json:"index_generation"`
	IndexStrategy   string `json:"index_strategy"`
}

type controlSearchQueueResponse struct {
	Queued            int64   `json:"queued"`
	Processing        int64   `json:"processing"`
	Failed            int64   `json:"failed"`
	ExpiredLeases     int64   `json:"expired_leases"`
	AffectedTeamCount int64   `json:"affected_team_count"`
	OldestPendingAge  float64 `json:"oldest_pending_age_seconds"`
	OldestFailureAge  float64 `json:"oldest_failure_age_seconds"`
}

type controlSearchFailureResponse struct {
	SourceKind   string `json:"source_kind"`
	FailureClass string `json:"failure_class"`
	FailureCode  string `json:"failure_code"`
	Count        int64  `json:"count"`
}

type controlSearchFailureGroupResponse struct {
	TeamID             string  `json:"team_id"`
	TeamName           string  `json:"team_name"`
	SourceKind         string  `json:"source_kind"`
	FailureClass       string  `json:"failure_class"`
	FailureCode        string  `json:"failure_code"`
	Status             string  `json:"status"`
	FailedJobCount     int64   `json:"failed_job_count"`
	QueuedJobCount     int64   `json:"queued_job_count"`
	ProcessingJobCount int64   `json:"processing_job_count"`
	AffectedJobCount   int64   `json:"affected_job_count"`
	FirstFailedAt      string  `json:"first_failed_at"`
	LastFailedAt       string  `json:"last_failed_at"`
	AgeSeconds         float64 `json:"age_seconds"`
	Guidance           string  `json:"guidance"`
}

type controlSearchRunResponse struct {
	RunID              string `json:"run_id"`
	LocalRunDate       string `json:"local_run_date"`
	Status             string `json:"status"`
	CanaryJobID        string `json:"canary_job_id,omitempty"`
	CanaryAttemptedAt  string `json:"canary_attempted_at,omitempty"`
	CanaryOutcome      string `json:"canary_outcome"`
	CanaryFailureClass string `json:"canary_failure_class,omitempty"`
	CanaryFailureCode  string `json:"canary_failure_code,omitempty"`
	RequeuedCount      int64  `json:"requeued_count"`
	RecoveredCount     int64  `json:"recovered_count"`
	LastError          string `json:"last_error,omitempty"`
	UpdatedAt          string `json:"updated_at"`
	SelectedCount      int64  `json:"selected_count"`
	EmbeddedCount      int64  `json:"embedded_count"`
	UpdatedCount       int64  `json:"updated_count"`
	DriftedCount       int64  `json:"drifted_count"`
}

func toControlSearchConvergence(value *repository.SearchConvergence) controlSearchConvergenceResponse {
	response := controlSearchConvergenceResponse{Failures: []controlSearchFailureResponse{}, FailureGroups: []controlSearchFailureGroupResponse{}, DriftClasses: []controlSearchDriftClassResponse{}}
	if value == nil {
		return response
	}
	response.ObservedAt = value.ObservedAt.UTC().Format(time.RFC3339)
	response.Status = value.Status
	response.ExpectedDocuments = value.ExpectedDocuments
	response.CurrentDocuments = value.CurrentDocuments
	response.DriftedDocuments = value.DriftedDocuments
	response.AffectedTeamCount = value.AffectedTeamCount
	response.OldestDriftAge = value.OldestDriftAge.Seconds()
	for _, drift := range value.DriftClasses {
		response.DriftClasses = append(response.DriftClasses, controlSearchDriftClassResponse{Class: drift.Class, Count: drift.Count})
	}
	if value.Contract != nil {
		response.Contract = &controlSearchContractResponse{
			Provider: value.Contract.EmbeddingProvider, Model: value.Contract.EmbeddingModel,
			Dimensions: value.Contract.EmbeddingDimensions, IndexGeneration: value.Contract.IndexGeneration,
			IndexStrategy: value.Contract.IndexStrategy,
		}
	}
	response.Queue = controlSearchQueueResponse{
		Queued: value.Queued, Processing: value.Processing, Failed: value.Failed,
		ExpiredLeases: value.ExpiredLeases, AffectedTeamCount: value.QueueAffectedTeamCount,
		OldestPendingAge: value.OldestPendingAge.Seconds(), OldestFailureAge: value.OldestFailureAge.Seconds(),
	}
	for _, failure := range value.Failures {
		response.Failures = append(response.Failures, controlSearchFailureResponse{SourceKind: failure.SourceKind, FailureClass: failure.FailureClass, FailureCode: failure.FailureCode, Count: failure.Count})
	}
	for _, group := range value.FailureGroups {
		response.FailureGroups = append(response.FailureGroups, controlSearchFailureGroupResponse{
			TeamID: group.TeamID, TeamName: group.TeamName,
			SourceKind: group.SourceKind, FailureClass: group.FailureClass, FailureCode: group.FailureCode,
			Status: group.Status, FailedJobCount: group.FailedJobCount,
			QueuedJobCount: group.QueuedJobCount, ProcessingJobCount: group.ProcessingJobCount,
			AffectedJobCount: group.AffectedJobCount,
			FirstFailedAt:    group.FirstFailedAt.UTC().Format(time.RFC3339), LastFailedAt: group.LastFailedAt.UTC().Format(time.RFC3339),
			AgeSeconds: group.Age.Seconds(), Guidance: group.Guidance,
		})
	}
	response.FailureGroupCount = value.FailureGroupCount
	response.FailureGroupsTruncated = value.FailureGroupsTruncated
	if run := value.LatestRun; run != nil {
		response.LatestRun = &controlSearchRunResponse{
			RunID: run.RunID, LocalRunDate: run.LocalRunDate.Format("2006-01-02"), Status: run.Status,
			CanaryJobID: run.CanaryJobID, CanaryOutcome: run.CanaryOutcome,
			CanaryFailureClass: run.CanaryFailureClass, CanaryFailureCode: run.CanaryFailureCode,
			RequeuedCount: run.RequeuedCount, RecoveredCount: run.RecoveredCount,
			SelectedCount: run.SelectedCount, EmbeddedCount: run.EmbeddedCount,
			UpdatedCount: run.UpdatedCount, DriftedCount: run.DriftedCount,
			UpdatedAt: run.UpdatedAt.UTC().Format(time.RFC3339),
		}
		response.LatestRun.LastError = boundedControlSearchRunError(run.LastError, run.CanaryFailureCode)
		if run.CanaryAttemptedAt != nil {
			response.LatestRun.CanaryAttemptedAt = run.CanaryAttemptedAt.UTC().Format(time.RFC3339)
		}
	}
	return response
}

func boundedControlSearchRunError(message, failureCode string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if message == "" {
		return ""
	}
	if failureCode != "" {
		return "reconciliation failed: " + failureCode
	}
	if isControlSearchRepairErrorCode(message) {
		return "reconciliation failed: " + message
	}
	const limit = 128
	if len(message) > limit {
		return "reconciliation operation failed"
	}
	switch message {
	case "canary selection failed",
		"daily embedding canary failure persistence was ambiguous",
		"daily embedding canary completion was ambiguous",
		"reconciliation backlog release failed",
		"daily embedding canary failed":
		return message
	default:
		return "reconciliation operation failed"
	}
}

func isControlSearchRepairErrorCode(value string) bool {
	switch value {
	case "embedding_timeout",
		"embedding_cancelled",
		"embedding_unavailable",
		"embedding_response_invalid",
		"embedding_contract_mismatch",
		"reconciliation_selection_failed",
		"reconciliation_snapshot_invalid",
		"reconciliation_commit_failed",
		"reconciliation_count_failed":
		return true
	default:
		return false
	}
}
