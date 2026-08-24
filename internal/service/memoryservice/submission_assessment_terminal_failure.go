package memoryservice

import (
	"context"
	"errors"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
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
	defaultRelationshipResultReason := ""
	if relationshipResults != nil {
		defaultRelationshipResultReason = terminalRelationshipResultFallback(
			string(domain.SemanticReviewTerminalFailure), relationshipResults,
		)
	}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope:    scope,
		OutcomeKind:                     "submission_assessment_terminal",
		Status:                          string(domain.SemanticReviewTerminalFailure),
		Category:                        "failed",
		Payload:                         payload,
		RelationshipResults:             relationshipResults,
		DefaultRelationshipResultReason: defaultRelationshipResultReason,
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
		cause := firstError(failureCause)
		if isRepositoryDatabaseFailure(cause) {
			return s.completeTerminalWithoutRelationshipResults(ctx, scope, stage, cause)
		}
		return s.completeTerminalWithRelationshipResults(
			ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage,
			relationshipResults, cause,
		)
	}
	return s.retryOrFail(ctx, run, scope, stage, providerAttempted, releaseProviderAttempt, failureCause...)
}

func (s *submissionAssessmentPlacementWorkerService) retryProviderFailureWithTerminal(
	ctx context.Context,
	run repository.PlacementRun,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	releaseProviderAttempt bool,
	assessorTurnsReserved int,
	failure assessor.ProviderFailureMetadata,
) error {
	if run.MaxAttempts > 0 && run.Attempts >= run.MaxAttempts {
		return s.completeTerminalWithFailure(
			ctx, scope, stage, failure.Class, failure.StatusCode, assessorTurnsReserved,
		)
	}
	return s.retryProviderFailure(ctx, run, scope, stage, releaseProviderAttempt, assessorTurnsReserved, failure)
}

func (s *submissionAssessmentPlacementWorkerService) completeTerminalWithoutRelationshipResults(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	stage string,
	failureCause ...error,
) error {
	return s.completeTerminalWithRelationshipResultsAndReason(
		ctx, scope, string(domain.SemanticReviewTerminalFailure), "failed", stage,
		[]repository.SubmissionRelationshipResultInput{}, "", firstError(failureCause),
	)
}

func (s *submissionAssessmentPlacementWorkerService) completeTerminalWithRelationshipResults(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	status, category, stage string,
	relationshipResults []repository.SubmissionRelationshipResultInput,
	failureCause error,
) error {
	return s.completeTerminalWithRelationshipResultsAndReason(
		ctx, scope, status, category, stage, relationshipResults,
		terminalRelationshipResultFallback(status, relationshipResults), failureCause,
	)
}

func (s *submissionAssessmentPlacementWorkerService) completeTerminalWithRelationshipResultsAndReason(
	ctx context.Context,
	scope repository.SubmissionAssessmentRunScope,
	status, category, stage string,
	relationshipResults []repository.SubmissionRelationshipResultInput,
	defaultRelationshipResultReason string,
	failureCause error,
) error {
	var payload map[string]any
	if status == string(domain.SemanticReviewSuperseded) {
		payload = map[string]any{"assessor_contract": domain.ContractVersion}
	} else {
		payload = semanticAssessmentFailurePayload(stage, false, failureCause)
		payload["assessor_contract"] = domain.ContractVersion
	}
	failureClass, _ := payload["failure_class"].(string)
	failureCode := submissionFailureCode(stage, failureClass)
	if status != string(domain.SemanticReviewSuperseded) {
		payload["failure_code"] = string(failureCode)
	}
	completed, err := s.assessments.CompleteSubmissionAssessment(ctx, repository.CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope:    scope,
		OutcomeKind:                     "submission_assessment_terminal",
		Status:                          status,
		Category:                        category,
		Payload:                         payload,
		RelationshipResults:             relationshipResults,
		DefaultRelationshipResultReason: defaultRelationshipResultReason,
	})
	if err == nil && completed == nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, errors.New("submission assessment worker: nil terminal result"))
	}
	if err != nil {
		return newPlacementWorkerError(scope.TeamID, scope.IngestID, stage, err)
	}
	if err == nil && completed != nil && status == string(domain.SemanticReviewTerminalFailure) {
		observability.RecordAssessorTerminalFailure(s.metrics, stage)
	}
	if err == nil && completed != nil {
		event, destination, reasonCode := "submission_failed", "failed", string(failureCode)
		if failureClass != "" {
			reasonCode = string(submissionFailureCode(stage, failureClass))
		}
		if status == string(domain.SemanticReviewSuperseded) {
			event, destination, reasonCode = "submission_superseded", "superseded", strings.TrimSpace(stage)
		}
		s.logLifecycle(scope, event, destination, stage, reasonCode, nil)
	}
	return err
}

func terminalRelationshipResultFallback(
	status string,
	results []repository.SubmissionRelationshipResultInput,
) string {
	if status == string(domain.SemanticReviewTerminalFailure) && len(results) == 0 {
		return string(SubmissionErrorInternalFailure)
	}
	return ""
}
