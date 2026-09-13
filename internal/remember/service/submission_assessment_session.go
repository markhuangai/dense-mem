package service

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/observability"
)

type submissionAssessmentValidationTurn struct {
	Attempt    int
	Stage      string
	Fields     []string
	ErrorCount int
}

type submissionAssessmentValidationHistoryError struct {
	cause error
	turns []submissionAssessmentValidationTurn
}

func (err *submissionAssessmentValidationHistoryError) Error() string {
	if err == nil || err.cause == nil {
		return "semantic assessor validation failed"
	}
	return err.cause.Error()
}

func (err *submissionAssessmentValidationHistoryError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func wrapSubmissionAssessmentValidationHistory(err error, turns []submissionAssessmentValidationTurn) error {
	if err == nil || len(turns) == 0 {
		return err
	}
	copyTurns := make([]submissionAssessmentValidationTurn, len(turns))
	copy(copyTurns, turns)
	return &submissionAssessmentValidationHistoryError{cause: err, turns: copyTurns}
}

func preserveSubmissionAssessmentValidationHistory(mapped, original error) error {
	if mapped == nil || original == nil {
		return mapped
	}
	var historyErr *submissionAssessmentValidationHistoryError
	if !errors.As(original, &historyErr) || historyErr == nil || len(historyErr.turns) == 0 {
		return mapped
	}
	var mappedHistory *submissionAssessmentValidationHistoryError
	if errors.As(mapped, &mappedHistory) {
		return mapped
	}
	return wrapSubmissionAssessmentValidationHistory(mapped, historyErr.turns)
}

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
	return s.assessRememberSessionWithValidator(ctx, request, refresh, turnOffset, nil)
}

type submissionAssessmentResponseValidator func(assessor.SemanticAssessmentRequest, assessor.SemanticAssessmentResponse) []assessor.SemanticValidationError

func (s *assessmentEngine) assessRememberSessionWithValidator(
	ctx context.Context,
	request assessor.SemanticAssessmentRequest,
	refresh func(context.Context) (assessor.SemanticAssessmentRequest, error),
	turnOffset int,
	validate submissionAssessmentResponseValidator,
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
	response, finalRequest, err := s.completeRememberSessionTurnsWithValidator(ctx, session, turn, request, refresh, turnOffset, validate)
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
	return s.completeRememberSessionTurnsWithValidator(ctx, session, turn, request, refresh, turnOffset, nil)
}

func (s *assessmentEngine) completeRememberSessionTurnsWithValidator(
	ctx context.Context,
	session assessor.SemanticAssessmentSession,
	turn assessor.SemanticAssessmentTurn,
	request assessor.SemanticAssessmentRequest,
	refresh func(context.Context) (assessor.SemanticAssessmentRequest, error),
	turnOffset int,
	validate submissionAssessmentResponseValidator,
) (assessor.SemanticAssessmentResponse, assessor.SemanticAssessmentRequest, error) {
	if s == nil || s.provider == nil {
		return assessor.SemanticAssessmentResponse{}, request, errors.New("synchronous assessment provider is required")
	}
	var validationHistory []submissionAssessmentValidationTurn
	validationAttempts := 0
	for {
		validationAttempts++
		totalTurns := turnOffset + validationAttempts
		response := turn.Response
		validationErrors := append([]assessor.SemanticValidationError(nil), turn.ValidationErrors...)
		if len(validationErrors) == 0 {
			response, validationErrors = assessor.PrepareSemanticAssessmentResponse(request, response, s.limits)
		}
		if len(validationErrors) == 0 && validate != nil {
			validationErrors = validate(request, response)
		}
		if len(validationErrors) == 0 {
			response.ProviderTurns = totalTurns
			return response, request, nil
		}
		observability.RecordAssessorValidationFailure(s.metrics, assessmentValidationStage(turn.ValidationStage))
		for _, family := range semanticAssessmentValidationFieldFamiliesForService(validationErrors) {
			observability.RecordAssessorValidationFieldFailure(s.metrics, assessmentValidationStage(turn.ValidationStage), family)
		}
		validationFields := make([]string, 0, len(validationErrors))
		for _, validationError := range validationErrors {
			validationFields = append(validationFields, validationError.Field)
		}
		validationHistory = append(validationHistory, submissionAssessmentValidationTurn{
			Attempt:    totalTurns,
			Stage:      assessmentValidationStage(turn.ValidationStage),
			Fields:     validationFields,
			ErrorCount: len(validationErrors),
		})
		if totalTurns >= SemanticMaxAssessorTurns {
			failure := &assessor.MalformedResponseError{
				Provider:                "semantic_assessor",
				Message:                 "semantic assessor response remained invalid after bounded correction",
				FailureClass:            "malformed_exhausted",
				Attempts:                totalTurns,
				ValidationStage:         assessmentValidationStage(turn.ValidationStage),
				ValidationFieldFamilies: semanticAssessmentValidationFieldFamiliesForService(validationErrors),
			}
			return assessor.SemanticAssessmentResponse{}, request, wrapSubmissionAssessmentValidationHistory(failure, validationHistory)
		}
		nextRequest, err := refresh(ctx)
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, request, &submissionAssessmentConsumedTurnsError{cause: wrapSubmissionAssessmentValidationHistory(err, validationHistory), providerTurns: totalTurns}
		}
		turn, err = s.provider.Repair(ctx, session, assessor.SemanticAssessmentRepairRequest{
			Request: nextRequest, ValidationErrors: validationErrors,
		})
		if err != nil {
			return assessor.SemanticAssessmentResponse{}, request, &submissionAssessmentConsumedTurnsError{cause: wrapSubmissionAssessmentValidationHistory(err, validationHistory), providerTurns: totalTurns}
		}
		request = nextRequest
	}
}
