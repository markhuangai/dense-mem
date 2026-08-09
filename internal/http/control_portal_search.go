package http

import (
	nethttp "net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func (h *controlPortalHandler) getSearchConvergence(c echo.Context) error {
	if h.convergence == nil {
		return httperr.New(httperr.SERVICE_UNAVAILABLE, "search convergence unavailable")
	}
	projection, err := h.convergence.GetSearchConvergence(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(nethttp.StatusOK, map[string]any{"data": toControlSearchConvergence(projection)})
}

type controlSearchConvergenceResponse struct {
	ObservedAt string                          `json:"observed_at"`
	Status     string                          `json:"status"`
	Contract   *controlSearchContractResponse  `json:"contract,omitempty"`
	Queue      controlSearchQueueResponse      `json:"queue"`
	Failures   []controlSearchFailureResponse  `json:"failures"`
	Incidents  []controlSearchIncidentResponse `json:"incidents"`
	LatestRun  *controlSearchRunResponse       `json:"latest_run,omitempty"`
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

type controlSearchIncidentResponse struct {
	TeamID           string  `json:"team_id"`
	TeamName         string  `json:"team_name"`
	IncidentID       string  `json:"incident_id"`
	SourceKind       string  `json:"source_kind"`
	FailureClass     string  `json:"failure_class"`
	FailureCode      string  `json:"failure_code"`
	Status           string  `json:"status"`
	AffectedJobCount int64   `json:"affected_job_count"`
	FirstSeenAt      string  `json:"first_seen_at"`
	LastSeenAt       string  `json:"last_seen_at"`
	AgeSeconds       float64 `json:"age_seconds"`
	Guidance         string  `json:"guidance"`
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
}

func toControlSearchConvergence(value *repository.SearchConvergence) controlSearchConvergenceResponse {
	response := controlSearchConvergenceResponse{Failures: []controlSearchFailureResponse{}, Incidents: []controlSearchIncidentResponse{}}
	if value == nil {
		return response
	}
	response.ObservedAt = value.ObservedAt.UTC().Format(time.RFC3339)
	response.Status = value.Status
	if value.Contract != nil {
		response.Contract = &controlSearchContractResponse{
			Provider: value.Contract.EmbeddingProvider, Model: value.Contract.EmbeddingModel,
			Dimensions: value.Contract.EmbeddingDimensions, IndexGeneration: value.Contract.IndexGeneration,
			IndexStrategy: value.Contract.IndexStrategy,
		}
	}
	response.Queue = controlSearchQueueResponse{
		Queued: value.Queued, Processing: value.Processing, Failed: value.Failed,
		ExpiredLeases: value.ExpiredLeases, AffectedTeamCount: value.AffectedTeamCount,
		OldestPendingAge: value.OldestPendingAge.Seconds(), OldestFailureAge: value.OldestFailureAge.Seconds(),
	}
	for _, failure := range value.Failures {
		response.Failures = append(response.Failures, controlSearchFailureResponse{SourceKind: failure.SourceKind, FailureClass: failure.FailureClass, FailureCode: failure.FailureCode, Count: failure.Count})
	}
	for _, incident := range value.Incidents {
		response.Incidents = append(response.Incidents, controlSearchIncidentResponse{
			TeamID: incident.TeamID, TeamName: incident.TeamName, IncidentID: incident.IncidentID,
			SourceKind: incident.SourceKind, FailureClass: incident.FailureClass, FailureCode: incident.FailureCode,
			Status: incident.Status, AffectedJobCount: incident.AffectedJobCount,
			FirstSeenAt: incident.FirstSeenAt.UTC().Format(time.RFC3339), LastSeenAt: incident.LastSeenAt.UTC().Format(time.RFC3339),
			AgeSeconds: incident.Age.Seconds(), Guidance: incident.Guidance,
		})
	}
	if run := value.LatestRun; run != nil {
		response.LatestRun = &controlSearchRunResponse{
			RunID: run.RunID, LocalRunDate: run.LocalRunDate.Format("2006-01-02"), Status: run.Status,
			CanaryJobID: run.CanaryJobID, CanaryOutcome: run.CanaryOutcome,
			CanaryFailureClass: run.CanaryFailureClass, CanaryFailureCode: run.CanaryFailureCode,
			RequeuedCount: run.RequeuedCount, RecoveredCount: run.RecoveredCount,
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
