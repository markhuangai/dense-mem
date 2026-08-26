package memoryservice

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type submissionAssessmentConsumedTurnsError struct {
	cause         error
	providerTurns int
}

func (err *submissionAssessmentConsumedTurnsError) Error() string {
	if err == nil || err.cause == nil {
		return "submission assessment session failed after consuming provider turns"
	}
	return err.cause.Error()
}

func (err *submissionAssessmentConsumedTurnsError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func submissionAssessmentConsumedProviderTurns(err error) int {
	var consumed *submissionAssessmentConsumedTurnsError
	if !errors.As(err, &consumed) || consumed == nil {
		return 0
	}
	return consumed.providerTurns
}

// SynchronousAssessmentProviderTurns returns the bounded provider-turn count
// retained by a failed synchronous assessment without exposing provider text.
func SynchronousAssessmentProviderTurns(err error) int {
	turns := submissionAssessmentConsumedProviderTurns(err)
	var malformed *assessor.MalformedResponseError
	if errors.As(err, &malformed) && malformed != nil && malformed.Attempts > turns {
		turns = malformed.Attempts
	}
	if turns < 0 {
		return 0
	}
	if turns > SemanticMaxAssessorTurns {
		return SemanticMaxAssessorTurns
	}
	return turns
}

func (s *assessmentEngine) assessRememberSession(
	ctx context.Context,
	request assessor.SemanticAssessmentRequest,
	refresh func(context.Context) (assessor.SemanticAssessmentRequest, error),
	turnOffset int,
) (assessor.SemanticAssessmentResponse, assessor.SemanticAssessmentSession, assessor.SemanticAssessmentRequest, error) {
	if s == nil || s.provider == nil {
		return assessor.SemanticAssessmentResponse{}, nil, request, errors.New("synchronous assessment provider is required")
	}
	if refresh == nil {
		return assessor.SemanticAssessmentResponse{}, nil, request, errors.New("synchronous assessment refresh is required")
	}
	session, turn, err := s.provider.Assess(ctx, request)
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, session, request, err
	}
	response, finalRequest, err := s.completeRememberSessionTurns(ctx, session, turn, request, refresh, turnOffset)
	return response, session, finalRequest, err
}

func (s *assessmentEngine) completeRememberSessionTurns(
	ctx context.Context,
	session assessor.SemanticAssessmentSession,
	turn assessor.SemanticAssessmentTurn,
	request assessor.SemanticAssessmentRequest,
	refresh func(context.Context) (assessor.SemanticAssessmentRequest, error),
	turnOffset int,
) (assessor.SemanticAssessmentResponse, assessor.SemanticAssessmentRequest, error) {
	if s == nil || s.provider == nil {
		return assessor.SemanticAssessmentResponse{}, request, errors.New("synchronous assessment provider is required")
	}
	for {
		turnNumber := turn.Turn
		if turnNumber <= 0 {
			turnNumber = 1
		}
		totalTurns := turnOffset + turnNumber
		response := turn.Response
		validationErrors := append([]assessor.SemanticValidationError(nil), turn.ValidationErrors...)
		if len(validationErrors) == 0 {
			response, validationErrors = assessor.PrepareSemanticAssessmentResponse(request, response, s.limits)
		}
		if len(validationErrors) == 0 {
			response.ProviderTurns = totalTurns
			return response, request, nil
		}
		observability.RecordAssessorValidationFailure(s.metrics, assessmentValidationStage(turn.ValidationStage))
		for _, family := range semanticAssessmentValidationFieldFamiliesForService(validationErrors) {
			observability.RecordAssessorValidationFieldFailure(s.metrics, assessmentValidationStage(turn.ValidationStage), family)
		}
		if totalTurns >= SemanticMaxAssessorTurns {
			return assessor.SemanticAssessmentResponse{}, request, &assessor.MalformedResponseError{
				Provider:                "semantic_assessor",
				Message:                 "semantic assessor response remained invalid after bounded correction",
				FailureClass:            "malformed_exhausted",
				Attempts:                totalTurns,
				ValidationStage:         assessmentValidationStage(turn.ValidationStage),
				ValidationFieldFamilies: semanticAssessmentValidationFieldFamiliesForService(validationErrors),
			}
		}
		nextRequest, err := refresh(ctx)
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, request, &submissionAssessmentConsumedTurnsError{cause: err, providerTurns: totalTurns}
		}
		turn, err = s.provider.Repair(ctx, session, assessor.SemanticAssessmentRepairRequest{
			Request: nextRequest, ValidationErrors: validationErrors,
		})
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, request, &submissionAssessmentConsumedTurnsError{cause: err, providerTurns: totalTurns}
		}
		request = nextRequest
	}
}
