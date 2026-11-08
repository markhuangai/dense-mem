package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/conflict/assessment"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	ConflictAssessmentSchemaName            = conflictassessment.ConflictAssessmentSchemaName
	ConflictAssessmentDecisionSelect        = conflictassessment.ConflictAssessmentDecisionSelect
	ConflictAssessmentDecisionAbstain       = conflictassessment.ConflictAssessmentDecisionAbstain
	ConflictAssessmentMaxPositions          = conflictassessment.ConflictAssessmentMaxPositions
	ConflictAssessmentMaxEvidence           = conflictassessment.ConflictAssessmentMaxEvidence
	ConflictAssessmentMaxContent            = conflictassessment.ConflictAssessmentMaxContent
	ConflictAssessmentMaxRationale          = conflictassessment.ConflictAssessmentMaxRationale
	conflictAssessmentSystemPrompt          = conflictassessment.ConflictAssessmentSystemPrompt
	conflictAssessmentCorrectionInstruction = conflictassessment.ConflictAssessmentCorrectionInstruction
)

type ConflictAssessmentEvidence = conflictassessment.ConflictAssessmentEvidence
type ConflictAssessmentPosition = conflictassessment.ConflictAssessmentPosition
type ConflictAssessmentRequest = conflictassessment.ConflictAssessmentRequest
type ConflictAssessmentResponse = conflictassessment.ConflictAssessmentResponse

func ConflictAssessmentResponseSchema() map[string]any {
	return conflictassessment.ConflictAssessmentResponseSchema()
}

func PrepareConflictAssessmentRequest(
	req ConflictAssessmentRequest,
	limits SemanticAssessmentLimits,
) (ConflictAssessmentRequest, []SemanticValidationError) {
	prepared, errs := conflictassessment.PrepareConflictAssessmentRequest(req, assessor.SemanticAssessmentLimits(limits))
	return prepared, legacyConflictValidationErrors(errs)
}

func DecodeConflictAssessmentResponseJSON(
	data []byte,
	limits SemanticAssessmentLimits,
) (ConflictAssessmentResponse, error) {
	return conflictassessment.DecodeConflictAssessmentResponseJSON(data, assessor.SemanticAssessmentLimits(limits))
}

func validateConflictAssessmentResponse(
	req ConflictAssessmentRequest,
	response ConflictAssessmentResponse,
) []SemanticValidationError {
	return legacyConflictValidationErrors(conflictassessment.ValidateConflictAssessmentResponse(req, response))
}

func legacyConflictValidationErrors(errs []assessor.SemanticValidationError) []SemanticValidationError {
	if len(errs) == 0 {
		return nil
	}
	converted := make([]SemanticValidationError, 0, len(errs))
	for _, err := range errs {
		converted = append(converted, semanticErr(err.Field, err.Message))
	}
	return converted
}

// AssessRelationshipConflict keeps the transitional verifier API while the
// canonical Conflict request, response, schema, and validation contract lives
// in the conflictassessment package.
func (v *OpenAIVerifier) AssessRelationshipConflict(ctx context.Context, req ConflictAssessmentRequest) (final ConflictAssessmentResponse, finalErr error) {
	started := time.Now()
	attempts := 0
	var validation []SemanticValidationError
	attrs := []observability.LogAttr{observability.String("request_id", req.RequestID), observability.String("case_id", req.CaseID), observability.Int("case_version", req.Version), observability.String("model", v.model), observability.String("schema", ConflictAssessmentSchemaName)}
	defer func() {
		contextual, ok := v.logger.(observability.ContextLogProvider)
		if !ok {
			return
		}
		terminal := append(append([]observability.LogAttr{}, attrs...), observability.Int("attempts", attempts), observability.LogAttr{Key: "duration_ms", Value: time.Since(started).Milliseconds()})
		if finalErr != nil {
			terminal = append(terminal, observability.String("failure_class", ProviderFailureDetails(finalErr).Class), observability.LogAttr{Key: "validation_errors", Value: assessor.BoundSemanticAssessmentCorrectionErrors(validation)}, observability.String("outcome", "failed"))
			contextual.ErrorContext(ctx, "conflict_assessment_failed", finalErr, terminal...)
		} else {
			terminal = append(terminal, observability.String("decision", final.Decision), observability.String("outcome", "completed"))
			contextual.InfoContext(ctx, "conflict_assessment_completed", terminal...)
		}
	}()
	prepared, validationErrors := PrepareConflictAssessmentRequest(req, v.assessmentLimits)
	if len(validationErrors) > 0 {
		validation = validationErrors
		return ConflictAssessmentResponse{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid conflict assessment request: " + openAIValidationSummary(validationErrors),
			FailureClass: ProviderFailureClassProviderUnavailable,
		}
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return ConflictAssessmentResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to marshal conflict assessment request", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
	}
	messages := []openAIVerifierMessage{{Role: "system", Content: conflictAssessmentSystemPrompt}, {Role: "user", Content: string(payload)}}
	for turn := 1; turn <= SemanticAssessmentMaxProviderTurns; turn++ {
		inputTokens, err := semanticAssessmentMessageTokens(messages, v.assessmentLimits.Tokenizer)
		if err != nil {
			return ConflictAssessmentResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to count conflict assessment tokens", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
		}
		if inputTokens > v.assessmentLimits.MaxInputTokens {
			return ConflictAssessmentResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "conflict assessment exceeds input token budget", FailureClass: "input_budget", Attempts: turn - 1}
		}
		attempts = turn
		result, err := v.openAIStructuredChatMessagesJSONWithUsage(ctx, v.model, ConflictAssessmentSchemaName, ConflictAssessmentResponseSchema(), messages)
		if contextual, ok := v.logger.(observability.ContextLogProvider); ok {
			var usage map[string]any
			if result.ReportedUsage != nil {
				usage = map[string]any{"prompt_tokens": result.ReportedUsage.PromptTokens, "completion_tokens": result.ReportedUsage.CompletionTokens, "total_tokens": result.ReportedUsage.TotalTokens}
			}
			fields := append(append([]observability.LogAttr{}, attrs...), observability.Int("attempt", turn), observability.LogAttr{Key: "transport", Value: result.Diagnostics.LogFields()}, observability.LogAttr{Key: "usage_available", Value: result.ReportedUsage != nil}, observability.LogAttr{Key: "reported_usage", Value: usage})
			contextual.DebugContext(ctx, "conflict_assessment_provider_response", fields...)
		}
		if err != nil {
			return ConflictAssessmentResponse{}, err
		}
		if result.ReportedUsage != nil && result.ReportedUsage.PromptTokens > int64(v.assessmentLimits.MaxInputTokens) {
			return ConflictAssessmentResponse{}, &MalformedResponseError{
				Provider:     openAIVerifierProvider,
				Message:      "provider reported input tokens beyond conflict assessment limit",
				FailureClass: "input_budget",
				Attempts:     turn,
			}
		}
		responseErrors := []SemanticValidationError{}
		response := ConflictAssessmentResponse{}
		if result.ReportedUsage != nil && result.ReportedUsage.CompletionTokens > int64(v.assessmentLimits.MaxOutputTokens) {
			responseErrors = append(responseErrors, SemanticValidationError{
				Field:   "output_tokens",
				Message: fmt.Sprintf("provider reported more than the allowed %d tokens", v.assessmentLimits.MaxOutputTokens),
			})
		} else {
			decoded, decodeErr := DecodeConflictAssessmentResponseJSON([]byte(result.Content), v.assessmentLimits)
			if decodeErr != nil {
				responseErrors = append(responseErrors, SemanticValidationError{Field: "response", Message: "must be one complete JSON object matching the required field types"})
			} else {
				response = decoded
				responseErrors = append(responseErrors, validateConflictAssessmentResponse(prepared, response)...)
			}
		}
		if len(responseErrors) == 0 {
			response.InputTokens = inputTokens
			if result.ReportedUsage != nil && result.ReportedUsage.PromptTokens > 0 {
				response.InputTokens = int(result.ReportedUsage.PromptTokens)
			}
			response.ProviderTurns = turn
			return response, nil
		}
		validation = responseErrors
		if contextual, ok := v.logger.(observability.ContextLogProvider); ok {
			fields := append(append([]observability.LogAttr{}, attrs...), observability.Int("attempt", turn), observability.LogAttr{Key: "validation_errors", Value: assessor.BoundSemanticAssessmentCorrectionErrors(responseErrors)})
			contextual.DebugContext(ctx, "conflict_assessment_response_invalid", fields...)
		}
		if turn == SemanticAssessmentMaxProviderTurns {
			return ConflictAssessmentResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "conflict assessment response remained invalid after bounded correction", FailureClass: "malformed_exhausted", Attempts: turn}
		}
		correction, err := json.Marshal(map[string]any{
			"validation_errors": assessor.BoundSemanticAssessmentCorrectionErrors(responseErrors),
			"instruction":       conflictAssessmentCorrectionInstruction,
		})
		if err != nil {
			return ConflictAssessmentResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to marshal conflict assessment correction", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
		}
		messages = append(messages, openAIVerifierMessage{Role: "assistant", Content: result.Content}, openAIVerifierMessage{Role: "user", Content: string(correction)})
	}
	return ConflictAssessmentResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "conflict assessment response remained invalid after bounded correction", FailureClass: "malformed_exhausted", Attempts: SemanticAssessmentMaxProviderTurns}
}
