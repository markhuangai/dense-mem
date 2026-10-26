package conflictreview

import (
	"context"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/conflict/assessment"
	repository "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func (s *Service) decisionSnapshot(ctx context.Context) (*domain.DecisionRuntimeConfig, error) {
	if s.decisions == nil {
		return nil, nil
	}
	if s.decisionSettings == nil {
		return nil, errors.New("conflict review: decision settings are unavailable")
	}
	snapshot, err := s.decisionSettings.DecisionRuntimeConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("conflict review: decision settings: %w", err)
	}
	return &snapshot, nil
}

func (s *Service) assessConflict(ctx context.Context, request conflictassessment.ConflictAssessmentRequest, snapshot *domain.DecisionRuntimeConfig) (conflictassessment.ConflictAssessmentResponse, error) {
	if snapshot == nil || !snapshot.Effective.ConflictReview {
		return s.provider.AssessRelationshipConflict(ctx, request)
	}
	choices := make([]modelprovider.DecisionOption, 0, len(request.Positions))
	for _, position := range request.Positions {
		choices = append(choices, modelprovider.DecisionOption{Value: position.PositionID, Description: position.PositionKey})
	}
	decisionRequest := modelprovider.DecisionRequest{Workload: "conflict_review", OperationID: request.RequestID, SettingsVersion: snapshot.Revision, Groups: []modelprovider.DecisionGroup{{ID: "dossier", Input: request, Questions: []modelprovider.DecisionQuestion{
		{Name: "position", Type: modelprovider.DecisionChoice, Instructions: conflictassessment.ConflictAssessmentSystemPrompt + " Choose the best-supported supplied position. A separate question tests whether any answer is sufficiently supported.", Choices: choices},
		{Name: "sufficiently_supported", Type: modelprovider.DecisionPredicate, Instructions: conflictassessment.ConflictAssessmentSystemPrompt + " Does the complete dossier establish a clear, sufficiently supported answer to the conflict question?"},
	}}}}
	inputHash, err := modelprovider.DecisionInputHash(decisionRequest.Groups)
	if err != nil {
		return conflictassessment.ConflictAssessmentResponse{}, err
	}
	stage := &modelprovider.DecisionStage{Workload: "conflict_review", InputHash: inputHash, Settings: *snapshot, PolicyRevision: modelprovider.DecisionPolicyRevision, Thresholds: map[string]float64{"position_probability": 0.99, "sufficiently_supported_probability": 0.99}, Action: "fail_required_task", Reason: "provider_failure"}
	result, err := s.decisions.Decide(ctx, decisionRequest)
	stage.Result = result
	response := conflictassessment.ConflictAssessmentResponse{DecisionStage: stage}
	if ctx.Err() != nil {
		return response, ctx.Err()
	}
	if err == nil {
		err = modelprovider.ValidateDecisionResult(decisionRequest, result)
	}
	if err != nil {
		observability.LogDecisionStage(ctx, s.logger, request.RequestID, *stage)
		return response, err
	}
	position, supported := result.Answers["position"], result.Answers["sufficiently_supported"]
	probability := position.Probabilities[position.Choice]
	if probability >= 0.99 && *supported.Probability >= 0.99 {
		stage.Action, stage.Reason = "select_typed_position", "position_and_support_probability_gates_met"
		response.Decision = conflictassessment.ConflictAssessmentDecisionSelect
		response.PositionID = &position.Choice
		response.SelectionProbability = &probability
		response.Rationale = "Server policy typed-decisions-v1 selected the supplied position because position and sufficiently-supported probabilities both met 0.99. This is a policy reason."
		response.ProviderTurns = len(result.Receipts)
	} else {
		stage.Action, stage.Reason = "generate_complete_conflict_review", "uncertain_position_or_support"
		response, err = s.provider.AssessRelationshipConflict(ctx, request)
		response.DecisionStage = stage
	}
	observability.LogDecisionStage(ctx, s.logger, request.RequestID, *stage)
	return response, err
}

func (s *Service) recordRequiredDecisionFailure(ctx context.Context, result *repository.ReviewRelationshipConflictCaseResult, input repository.ReviewRelationshipConflictCaseInput, reservation *repository.OverdueConflictAssessmentReservation, failure error, stage *modelprovider.DecisionStage) (*repository.ReviewRelationshipConflictCaseResult, error) {
	completion := repository.CompleteOverdueConflictAssessmentInput{Decision: "failed", FailureClass: conflictAssessmentFailureClass(failure)}
	if stage != nil {
		completion.DecisionDiagnostics = stage
	}
	if _, err := s.completeAssessment(ctx, input, reservation, completion); err != nil {
		return nil, errors.Join(failure, err)
	}
	result.Stage = "overdue_assessment_failed"
	return result, failure
}
