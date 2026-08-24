package memoryservice

import (
	"context"
	"errors"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

var errSubmissionLifecycleFailure = errors.New("submission terminal failure")

type submissionLifecycleEvent struct {
	Event         string
	TeamID        string
	ProfileID     string
	CorrelationID string
	SubmissionID  string
	From          string
	To            string
	Stage         string
	ReasonCode    string
	Attempts      int
	MaxAttempts   int
	NextAttemptAt *time.Time
}

func logSubmissionLifecycle(logger observability.LogProvider, event submissionLifecycleEvent) {
	if logger == nil {
		return
	}
	attrs := []observability.LogAttr{
		observability.String("event", event.Event),
		observability.String("team_id", event.TeamID),
		observability.ProfileID(event.ProfileID),
		observability.String("reference_type", "submission"),
		observability.String("reference_id", event.SubmissionID),
		observability.String("from", event.From),
		observability.String("to", event.To),
		observability.Int("attempts", event.Attempts),
	}
	if event.MaxAttempts > 0 {
		attrs = append(attrs, observability.Int("max_attempts", event.MaxAttempts))
	}
	if event.CorrelationID != "" {
		attrs = append(attrs, observability.CorrelationID(event.CorrelationID))
	}
	if event.Stage != "" {
		attrs = append(attrs, observability.String("stage", event.Stage))
	}
	if event.ReasonCode != "" {
		attrs = append(attrs, observability.String("reason_code", event.ReasonCode))
	}
	if event.NextAttemptAt != nil {
		attrs = append(attrs, observability.String("next_attempt_at", event.NextAttemptAt.UTC().Format(time.RFC3339Nano)))
	}
	switch event.Event {
	case "submission_accepted", "submission_completed":
		logger.Info(event.Event, attrs...)
	case "submission_failed":
		logger.Error(event.Event, errSubmissionLifecycleFailure, attrs...)
	default:
		logger.Warn(event.Event, attrs...)
	}
}

func submissionAssessmentRunScope(run repository.PlacementRun, workerID string) repository.SubmissionAssessmentRunScope {
	return repository.SubmissionAssessmentRunScope{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		CorrelationID:    run.CorrelationID,
		WorkerID:         workerID,
		ExpectedAttempts: run.Attempts,
		MaxAttempts:      run.MaxAttempts,
	}
}

func (s *submissionAssessmentPlacementWorkerService) recordFirstDisposition(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	disposition *repository.PlacementFirstDisposition,
) {
	if disposition == nil || !disposition.IsRemember {
		return
	}
	metricCtx := observability.WithMetricIdentity(ctx, teamID, ownerProfileID)
	observability.RecordRememberFirstDisposition(metricCtx, s.metrics, disposition.CompletedAt.Sub(disposition.CreatedAt), disposition.Status)
}

func (s *submissionAssessmentPlacementWorkerService) logLifecycle(
	scope repository.SubmissionAssessmentRunScope,
	event, to, stage, reasonCode string,
	nextAttemptAt *time.Time,
) {
	logSubmissionLifecycle(s.logger, submissionLifecycleEvent{
		Event:         event,
		TeamID:        scope.TeamID,
		ProfileID:     scope.OwnerProfileID,
		CorrelationID: scope.CorrelationID,
		SubmissionID:  scope.IngestID,
		From:          "processing",
		To:            to,
		Stage:         stage,
		ReasonCode:    reasonCode,
		Attempts:      scope.ExpectedAttempts,
		MaxAttempts:   scope.MaxAttempts,
		NextAttemptAt: nextAttemptAt,
	})
}
