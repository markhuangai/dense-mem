package memoryservice

import (
	"context"
	"errors"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
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
