package memoryservice

import (
	"context"
	"errors"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func (s *semanticAssessmentPlacementWorkerService) retryProviderFailure(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	stage string,
	releaseProviderAttempt bool,
	failure verifier.ProviderFailureMetadata,
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminalWithFailure(ctx, run, item, stage, failure.Class, failure.StatusCode, 0)
	}
	requeued, err := s.commit.RequeuePlacementReviewResult(ctx, repository.RequeuePlacementReviewInput{
		TeamID:                 run.TeamID,
		OwnerProfileID:         run.OwnerProfileID,
		IngestID:               run.IngestID,
		PlacementRunID:         run.PlacementRunID,
		PlacementItemID:        item.PlacementItemID,
		WorkerID:               s.workerID,
		ExpectedAttempts:       run.Attempts,
		OutcomeKind:            "semantic_assessment_attempt",
		Payload:                semanticAssessmentRetryPayload(stage, true, failure),
		RetryAfter:             failure.RetryAfter,
		ReleaseAssessorAttempt: releaseProviderAttempt,
	})
	if err == nil && requeued != nil {
		s.recordFirstDisposition(ctx, run, requeued.FirstDisposition)
	}
	if err != nil {
		return newPlacementWorkerError(run.TeamID, run.IngestID, stage, err)
	}
	return err
}

func semanticAssessmentRetryPayload(
	stage string,
	providerAttempted bool,
	failure ...verifier.ProviderFailureMetadata,
) map[string]any {
	diagnostic := placementFailureDiagnosticFor(stage, nil)
	if len(failure) > 0 {
		diagnostic = placementFailureDiagnosticForProvider(stage, failure[0])
	}
	payload := diagnostic.payload(providerAttempted)
	payload["assessor_contract"] = domain.ContractVersion
	return payload
}

func semanticAssessmentFailurePayload(
	stage string,
	providerAttempted bool,
	cause error,
	failure ...verifier.ProviderFailureMetadata,
) map[string]any {
	diagnostic := placementFailureDiagnosticFor(stage, cause)
	if len(failure) > 0 {
		diagnostic = placementFailureDiagnosticForProvider(stage, failure[0])
		if cause != nil {
			fromCause := placementFailureDiagnosticFor(stage, cause)
			diagnostic.ValidationStage = fromCause.ValidationStage
			diagnostic.ValidationFieldFamilies = fromCause.ValidationFieldFamilies
			diagnostic.Measurement = fromCause.Measurement
			diagnostic.AssessorTurns = fromCause.AssessorTurns
		}
	}
	payload := diagnostic.payload(providerAttempted)
	payload["assessor_contract"] = domain.ContractVersion
	return payload
}

func (s *semanticAssessmentPlacementWorkerService) completeTerminalWithFailure(
	ctx context.Context,
	run repository.PlacementRun,
	item repository.PlacementItem,
	stage string,
	failureClass string,
	providerStatus int,
	providerTurns int,
	failureCause ...error,
) error {
	var cause error
	if len(failureCause) > 0 {
		cause = failureCause[0]
	}
	payload := semanticAssessmentFailurePayload(stage, true, cause)
	if failureClass = strings.TrimSpace(failureClass); failureClass != "" {
		failureClass = boundedPlacementFailureClass(failureClass)
		payload["failure_class"] = failureClass
		payload["failure_reason_code"] = placementFailureReasonCode(stage, failureClass)
	}
	if providerStatus > 0 {
		payload["provider_status"] = providerStatus
	}
	if providerTurns > 0 {
		payload["assessor_turns"] = providerTurns
	}
	completed, err := s.commit.CompletePlacementReviewResult(ctx, repository.CompletePlacementReviewInput{
		TeamID:           run.TeamID,
		OwnerProfileID:   run.OwnerProfileID,
		IngestID:         run.IngestID,
		PlacementRunID:   run.PlacementRunID,
		PlacementItemID:  item.PlacementItemID,
		WorkerID:         s.workerID,
		ExpectedAttempts: run.Attempts,
		Status:           string(domain.SemanticReviewTerminalFailure),
		Category:         "failed",
		Payload:          payload,
	})
	if err == nil && completed != nil {
		s.recordFirstDisposition(ctx, run, completed.FirstDisposition)
		observability.RecordAssessorTerminalFailure(s.metrics, stage)
	}
	if err != nil {
		return newPlacementWorkerError(run.TeamID, run.IngestID, stage, err)
	}
	return err
}

func semanticAssessmentMalformedFailure(err error) (string, int) {
	var malformed *verifier.MalformedResponseError
	if !errors.As(err, &malformed) {
		return "malformed_response", 0
	}
	failureClass := strings.TrimSpace(malformed.FailureClass)
	if failureClass == "" {
		failureClass = "malformed_response"
	}
	return failureClass, malformed.Attempts
}
