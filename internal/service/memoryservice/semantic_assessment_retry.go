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
	return err
}

func semanticAssessmentRetryPayload(
	stage string,
	providerAttempted bool,
	failure ...verifier.ProviderFailureMetadata,
) map[string]any {
	if !providerAttempted {
		return nil
	}
	payload := map[string]any{
		"assessor_contract":           domain.ContractVersion,
		"assessor_provider_attempted": true,
		"failure_stage":               strings.TrimSpace(stage),
	}
	if len(failure) > 0 {
		if class := strings.TrimSpace(failure[0].Class); class != "" {
			payload["failure_class"] = class
		}
		if failure[0].StatusCode > 0 {
			payload["provider_status"] = failure[0].StatusCode
		}
	}
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
) error {
	payload := map[string]any{
		"assessor_contract": domain.ContractVersion,
		"failure_stage":     strings.TrimSpace(stage),
	}
	if failureClass = strings.TrimSpace(failureClass); failureClass != "" {
		payload["failure_class"] = failureClass
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
