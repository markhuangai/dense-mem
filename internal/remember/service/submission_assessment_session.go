package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/generation"
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

type submissionAssessmentResponseValidator func(context.Context, assessor.SemanticAssessmentRequest, assessor.SemanticAssessmentResponse) ([]assessor.SemanticValidationError, error)

const repairInstruction = "Return one complete replacement object for the unchanged original Evidence and known context. Repair every validation error together. Copy only the supplied evidence IDs and boundary refs. Return all opposing conflict positions and exactly one equivalence result per candidate group. Never return a patch or explanation."

func (s *assessmentEngine) assessRememberSession(
	ctx context.Context,
	request assessor.SemanticAssessmentRequest,
	refresh func(context.Context) (assessor.SemanticAssessmentRequest, error),
	turnOffset int,
	validate submissionAssessmentResponseValidator,
) (assessor.SemanticAssessmentResponse, error) {
	if s == nil || s.provider == nil {
		return assessor.SemanticAssessmentResponse{}, errors.New("synchronous assessment provider is required")
	}
	if refresh == nil {
		return assessor.SemanticAssessmentResponse{}, errors.New("synchronous assessment refresh is required")
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, err
	}
	s.completedInputTokens, s.completedOutputTokens = 0, 0
	var validationHistory []submissionAssessmentValidationTurn
	completedTurns := turnOffset
	correction := repairInstruction
	if request.DecisionRouting == nil || request.DecisionRouting.Security == nil {
		correction += " Return exactly one security result per submitted Evidence ID."
	}
	correction += assessor.SemanticAssessmentGenerationInstruction(request)
	if request.DecisionRouting != nil && request.DecisionRouting.SkipAISecurityReview {
		correction += "\n\n" + assessor.SemanticAssessmentConflictContext
	} else {
		correction += "\n\n" + assessor.SemanticAssessmentContextPolicy
	}

	var logger *slog.Logger
	if root, ok := s.logger.(interface{ ContextSlog() *slog.Logger }); ok {
		logger = root.ContextSlog()
	}
	service := generation.Service[assessor.SemanticAssessmentResponse]{Transport: s.provider, Logger: logger}
	result, err := service.Run(ctx, generation.Task[assessor.SemanticAssessmentResponse]{
		ID: request.RequestID, Capability: "semantic_assessor", Tokenizer: s.limits.Tokenizer,
		Correction: correction, ResponsesConsumed: turnOffset,
		Request: modelprovider.StructuredRequest{
			Model: s.provider.ModelName(), SchemaName: assessor.SemanticAssessmentSchemaName,
			Schema:         assessor.SemanticAssessmentGenerationSchema(request, s.limits),
			Messages:       []modelprovider.Message{{Role: "system", Content: assessor.SemanticAssessmentPromptForGeneration(request)}, {Role: "user", Content: string(payload)}},
			Temperature:    modelprovider.StructuredTemperature(s.limits.ProviderTemperatureDisabled),
			MaxInputTokens: s.limits.MaxInputTokens, MaxOutputTokens: s.limits.MaxOutputTokens,
		},
		BeforeAttempt: func(ctx context.Context, _ int, _ int) (context.Context, error) {
			s.diagnostics.beginTurn(ctx)
			return ctx, nil
		},
		BeforeRepair: func(ctx context.Context, attempt generation.Attempt) error {
			next, err := refresh(ctx)
			if err != nil {
				return err
			}
			nextPayload, err := json.Marshal(next)
			if err != nil {
				return err
			}
			if next.TeamID != request.TeamID || next.OwnerProfileID != request.OwnerProfileID || !bytes.Equal(payload, nextPayload) ||
				!assessor.SameSemanticAssessmentRouting(request.DecisionRouting, next.DecisionRouting) {
				return &modelprovider.ProviderError{Provider: "semantic_assessor", Message: "repair changed the immutable assessment request", FailureClass: modelprovider.ProviderFailureClassRequestInvalid}
			}
			requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: string(ProgressRegeneratingAssessment), Attempt: attempt.Number + 1, Total: SemanticMaxAssessorTurns})
			return ctx.Err()
		},
		Validate: func(ctx context.Context, raw []byte) (assessor.SemanticAssessmentResponse, error) {
			ReportProgress(ctx, ProgressValidatingAssessment)
			reply, err := assessor.DecodeSemanticAssessmentGeneration(raw, request, s.limits)
			if err != nil {
				var rejected *jsonstrict.ValidationError
				if errors.As(err, &rejected) {
					return reply, &jsonstrict.ValidationError{Stage: "response_json", Errors: rejected.Errors, Cause: rejected.Cause}
				}
				return reply, &jsonstrict.ValidationError{Stage: "response_json", Errors: []jsonstrict.Issue{jsonstrict.ResponseJSONValidationError(err, "")}}
			}
			reply, issues := assessor.PrepareSemanticAssessmentResponse(request, reply, s.limits)
			if len(issues) == 0 && validate != nil {
				issues, err = validate(ctx, request, reply)
			}
			if len(issues) > 0 {
				return reply, &jsonstrict.ValidationError{Stage: "response_contract", Errors: issues}
			}
			return reply, err
		},
		Observe: func(ctx context.Context, attempt generation.Attempt) {
			s.diagnostics.finishGenerationAttempt(attempt)
			if attempt.Err == nil || attempt.ValidationElapsed > 0 || attempt.Provider.UsageAvailable {
				s.completedInputTokens += attempt.EffectiveInputTokens()
				output := attempt.OutputTokens
				if attempt.Provider.CompletionTokens > 0 {
					output = attempt.Provider.CompletionTokens
				}
				s.completedOutputTokens += output
			}
			if attempt.Err == nil || attempt.Validation != nil || attempt.OutputTokens > 0 {
				completedTurns = attempt.Number
			}
			if attempt.Validation == nil {
				return
			}
			issues := attempt.Validation.Errors
			stage := assessmentValidationStage(attempt.Validation.Stage)
			observability.RecordAssessorValidationFailure(s.metrics, stage)
			for _, family := range semanticAssessmentValidationFieldFamiliesForService(issues) {
				observability.RecordAssessorValidationFieldFailure(s.metrics, stage, family)
			}
			fields := make([]string, 0, len(issues))
			for _, issue := range issues {
				fields = append(fields, issue.Field)
			}
			validationHistory = append(validationHistory, submissionAssessmentValidationTurn{
				Attempt: attempt.Number, Stage: stage, Fields: fields, ErrorCount: len(issues),
			})
		},
	})
	if err != nil {
		var malformed *modelprovider.MalformedResponseError
		if errors.As(err, &malformed) {
			switch malformed.FailureClass {
			case "malformed_exhausted":
				last := result.Attempts[len(result.Attempts)-1].Validation
				malformed.ValidationFieldFamilies = semanticAssessmentValidationFieldFamiliesForService(last.Errors)
			case "input_budget":
				observability.RecordAssessorValidationFailure(s.metrics, "input_budget")
			}
		}
		wrapped := wrapSubmissionAssessmentValidationHistory(err, validationHistory)
		if completedTurns > 0 {
			wrapped = &submissionAssessmentConsumedTurnsError{cause: wrapped, providerTurns: completedTurns}
		}
		return assessor.SemanticAssessmentResponse{}, wrapped
	}
	last := result.Attempts[len(result.Attempts)-1]
	result.Value.InputTokens = last.EffectiveInputTokens()
	result.Value.ProviderTurns = last.Number
	return result.Value, nil
}
