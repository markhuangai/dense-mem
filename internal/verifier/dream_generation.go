package verifier

import (
	"context"
	"encoding/json"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/dreamgeneration"
)

const (
	DreamGenerationSchemaName              = dreamgeneration.DreamGenerationSchemaName
	DreamGenerationMaxPaths                = dreamgeneration.DreamGenerationMaxPaths
	DreamGenerationMaxPredicatesPerPath    = dreamgeneration.DreamGenerationMaxPredicatesPerPath
	DreamGenerationMaxEvidencePerPremise   = dreamgeneration.DreamGenerationMaxEvidencePerPremise
	DreamGenerationMaxEvidenceContentRunes = dreamgeneration.DreamGenerationMaxEvidenceContentRunes
	DreamGenerationMaxOutputs              = dreamgeneration.DreamGenerationMaxOutputs
	DreamGenerationMaxStatementRunes       = dreamgeneration.DreamGenerationMaxStatementRunes
	DreamGenerationMaxRationaleRunes       = dreamgeneration.DreamGenerationMaxRationaleRunes
	DreamGenerationMaxOutcomeRunes         = dreamgeneration.DreamGenerationMaxOutcomeRunes
	dreamGenerationSystemPrompt            = dreamgeneration.DreamGenerationSystemPrompt
	dreamGenerationCorrectionInstruction   = dreamgeneration.DreamGenerationCorrectionInstruction
)

type DreamGenerationNode = dreamgeneration.DreamGenerationNode
type DreamGenerationEvidence = dreamgeneration.DreamGenerationEvidence
type DreamGenerationPremise = dreamgeneration.DreamGenerationPremise
type DreamGenerationPredicate = dreamgeneration.DreamGenerationPredicate
type DreamGenerationPath = dreamgeneration.DreamGenerationPath
type DreamGenerationRequest = dreamgeneration.DreamGenerationRequest
type DreamGenerationProposal = dreamgeneration.DreamGenerationProposal
type DreamGenerationResponse = dreamgeneration.DreamGenerationResponse

func DreamGenerationResponseSchema() map[string]any {
	return dreamgeneration.DreamGenerationResponseSchema()
}

func PrepareDreamGenerationRequest(
	req DreamGenerationRequest,
	limits SemanticAssessmentLimits,
) (DreamGenerationRequest, []SemanticValidationError) {
	prepared, errs := dreamgeneration.PrepareDreamGenerationRequest(req, assessor.SemanticAssessmentLimits(limits))
	return prepared, legacyDreamValidationErrors(errs)
}

func DecodeDreamGenerationResponseJSON(
	data []byte,
	limits SemanticAssessmentLimits,
) (DreamGenerationResponse, error) {
	return dreamgeneration.DecodeDreamGenerationResponseJSON(data, assessor.SemanticAssessmentLimits(limits))
}

func PrepareDreamGenerationResponse(
	req DreamGenerationRequest,
	response DreamGenerationResponse,
) (DreamGenerationResponse, []SemanticValidationError) {
	prepared, errs := dreamgeneration.PrepareDreamGenerationResponse(req, response)
	return prepared, legacyDreamValidationErrors(errs)
}

func legacyDreamValidationErrors(errs []assessor.SemanticValidationError) []SemanticValidationError {
	if len(errs) == 0 {
		return nil
	}
	converted := make([]SemanticValidationError, 0, len(errs))
	for _, err := range errs {
		converted = append(converted, semanticErr(err.Field, err.Message))
	}
	return converted
}

// GenerateDreams keeps the transitional verifier API while the canonical
// Dream request, response, schema, and validation contract lives in the
// dreamgeneration package.
func (v *OpenAIVerifier) GenerateDreams(ctx context.Context, req DreamGenerationRequest) (DreamGenerationResponse, error) {
	prepared, validationErrors := PrepareDreamGenerationRequest(req, v.assessmentLimits)
	if len(validationErrors) > 0 {
		return DreamGenerationResponse{}, &ProviderError{
			Provider:     openAIVerifierProvider,
			Message:      "invalid dream generation request: " + openAIValidationSummary(validationErrors),
			FailureClass: ProviderFailureClassRequestInvalid,
		}
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return DreamGenerationResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to marshal dream generation request", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
	}
	messages := []openAIVerifierMessage{{Role: "system", Content: dreamGenerationSystemPrompt}, {Role: "user", Content: string(payload)}}
	totalInputTokens := 0
	totalOutputTokens := 0
	for turn := 1; turn <= SemanticAssessmentMaxProviderTurns; turn++ {
		inputTokens, err := semanticAssessmentMessageTokens(messages, v.assessmentLimits.Tokenizer)
		if err != nil {
			return DreamGenerationResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to count dream generation tokens", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
		}
		if inputTokens > v.assessmentLimits.MaxInputTokens {
			return DreamGenerationResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "dream generation exceeds input token budget", FailureClass: "input_budget", Attempts: turn - 1}
		}
		result, err := v.openAIStructuredChatMessagesJSONWithUsage(ctx, v.model, DreamGenerationSchemaName, DreamGenerationResponseSchema(), messages)
		if err != nil {
			return DreamGenerationResponse{}, err
		}
		outputTokens, err := CountTokens(result.Content, v.assessmentLimits.Tokenizer)
		if err != nil {
			return DreamGenerationResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to count dream generation output tokens", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
		}
		turnInputTokens := inputTokens
		turnOutputTokens := outputTokens
		if result.ReportedUsage != nil {
			if result.ReportedUsage.PromptTokens > 0 {
				turnInputTokens = int(result.ReportedUsage.PromptTokens)
			}
			if result.ReportedUsage.CompletionTokens > 0 {
				turnOutputTokens = int(result.ReportedUsage.CompletionTokens)
			}
		}
		totalInputTokens += turnInputTokens
		totalOutputTokens += turnOutputTokens
		responseErrors := []SemanticValidationError{}
		response := DreamGenerationResponse{}
		if result.ReportedUsage != nil && result.ReportedUsage.PromptTokens > int64(v.assessmentLimits.MaxInputTokens) {
			responseErrors = append(responseErrors, semanticErr("input_tokens", "provider reported input tokens beyond the configured limit"))
		}
		if result.ReportedUsage != nil && result.ReportedUsage.CompletionTokens > int64(v.assessmentLimits.MaxOutputTokens) {
			responseErrors = append(responseErrors, semanticErr("output_tokens", "provider reported output tokens beyond the configured limit"))
		}
		if len(responseErrors) == 0 {
			decoded, decodeErr := DecodeDreamGenerationResponseJSON([]byte(result.Content), v.assessmentLimits)
			if decodeErr != nil {
				responseErrors = append(responseErrors, semanticErr("response", "must be one complete JSON object matching the required field types"))
			} else {
				response, responseErrors = PrepareDreamGenerationResponse(prepared, decoded)
			}
		}
		if len(responseErrors) == 0 {
			response.InputTokens = totalInputTokens
			response.OutputTokens = totalOutputTokens
			response.ProviderTurns = turn
			return response, nil
		}
		if turn == SemanticAssessmentMaxProviderTurns {
			return DreamGenerationResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "dream generation response remained invalid after bounded correction", FailureClass: "malformed_exhausted", Attempts: turn}
		}
		correction, err := json.Marshal(map[string]any{
			"validation_errors": boundedSemanticAssessmentCorrectionErrors(responseErrors),
			"instruction":       dreamGenerationCorrectionInstruction,
		})
		if err != nil {
			return DreamGenerationResponse{}, &ProviderError{Provider: openAIVerifierProvider, Message: "failed to marshal dream generation correction", Cause: err, FailureClass: ProviderFailureClassProviderUnavailable}
		}
		messages = append(messages, openAIVerifierMessage{Role: "assistant", Content: result.Content}, openAIVerifierMessage{Role: "user", Content: string(correction)})
	}
	return DreamGenerationResponse{}, &MalformedResponseError{Provider: openAIVerifierProvider, Message: "dream generation response remained invalid after bounded correction", FailureClass: "malformed_exhausted", Attempts: SemanticAssessmentMaxProviderTurns}
}
