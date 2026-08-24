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

func (s *submissionAssessmentPlacementWorkerService) completeTerminalWithRelationshipResultsFailure(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	failureClass string,
	providerStatus int,
	providerTurns int,
	relationshipResults []repository.SubmissionRelationshipResultInput,
	failureCause ...error,
) error {
	failureCode := submissionFailureCode(stage, failureClass)
	payload := semanticAssessmentFailurePayload(stage, true, firstError(failureCause))
	payload["failure_code"] = string(failureCode)
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
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: scope,
		OutcomeKind:                  "submission_assessment_terminal",
		Status:                       string(domain.SemanticReviewTerminalFailure),
		Category:                     "failed",
		Payload:                      payload,
		RelationshipResults:          relationshipResults,
	})
	if err == nil && completed == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil terminal result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil && completed != nil {
		observability.RecordAssessorTerminalFailure(s.metrics, stage)
		s.logLifecycle(scope, "submission_failed", "failed", stage, string(failureCode), nil)
	}
	return err
}

func submissionAssessmentNotStoredRelationshipResultsForPlan(
	plan submissionAssessmentPlan,
	reason string,
) []repository.SubmissionRelationshipResultInput {
	results := make([]repository.SubmissionRelationshipResultInput, 0, len(plan.RelationshipTargets))
	for _, target := range plan.RelationshipTargets {
		results = append(results, repository.SubmissionRelationshipResultInput{
			RelationshipRef: target.Target.ProposalID,
			Disposition:     "not_stored",
			Reason:          reason,
		})
	}
	return results
}

func (s *submissionAssessmentPlacementWorkerService) retryOrFailWithRelationshipResults(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	providerAttempted bool,
	releaseProviderAttempt bool,
	relationshipResults []repository.SubmissionRelationshipResultInput,
	failureCause ...error,
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminalWithRelationshipResults(
			ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage,
			relationshipResults, firstError(failureCause),
		)
	}
	return s.retryOrFail(ctx, run, scope, stage, providerAttempted, releaseProviderAttempt, failureCause...)
}

func (s *submissionAssessmentPlacementWorkerService) retryProviderFailureWithRelationshipResults(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	releaseProviderAttempt bool,
	relationshipResults []repository.SubmissionRelationshipResultInput,
	failure verifier.ProviderFailureMetadata,
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminalWithRelationshipResultsFailure(
			ctx, scope, stage, failure.Class, failure.StatusCode, 0, relationshipResults,
		)
	}
	return s.retryProviderFailure(ctx, run, scope, stage, releaseProviderAttempt, failure)
}
