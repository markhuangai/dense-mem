package dreamgeneration

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

const DreamGenerationMaxProviderTurns = 5

// Provider owns the Dream structured-output conversation while the transport
// remains a provider-agnostic port.
type Provider struct {
	transport modelprovider.StructuredTransport
	model     string
	limits    assessor.SemanticAssessmentLimits
}

func NewProvider(transport modelprovider.StructuredTransport, model string, limits assessor.SemanticAssessmentLimits) *Provider {
	return &Provider{
		transport: transport,
		model:     strings.TrimSpace(model),
		limits:    normalizeLimits(limits),
	}
}

func (p *Provider) ModelName() string {
	if p == nil {
		return ""
	}
	return p.model
}

func (p *Provider) GenerateDreams(ctx context.Context, req DreamGenerationRequest) (DreamGenerationResponse, error) {
	if p == nil || p.transport == nil {
		return DreamGenerationResponse{}, &modelprovider.ProviderError{
			Provider:     "structured_dream",
			Message:      "dream generation transport is unavailable",
			FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
		}
	}
	prepared, validationErrors := PrepareDreamGenerationRequest(req, p.limits)
	if len(validationErrors) > 0 {
		return DreamGenerationResponse{}, &modelprovider.ProviderError{
			Provider:     "structured_dream",
			Message:      "invalid dream generation request: " + joinedErrors(validationErrors),
			FailureClass: modelprovider.ProviderFailureClassRequestInvalid,
		}
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return DreamGenerationResponse{}, &modelprovider.ProviderError{
			Provider: "structured_dream", Message: "failed to marshal dream generation request",
			Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
		}
	}
	messages := []modelprovider.Message{{Role: "system", Content: dreamGenerationSystemPrompt}, {Role: "user", Content: string(payload)}}
	inputTotal, outputTotal := 0, 0
	for turn := 1; turn <= DreamGenerationMaxProviderTurns; turn++ {
		inputTokens, err := graphMessageTokens(messages, p.limits.Tokenizer)
		if err != nil {
			return DreamGenerationResponse{}, &modelprovider.ProviderError{
				Provider: "structured_dream", Message: "failed to count dream generation tokens",
				Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
			}
		}
		if inputTokens > p.limits.MaxInputTokens {
			return DreamGenerationResponse{}, &modelprovider.MalformedResponseError{
				Provider: "structured_dream", Message: "dream generation exceeds input token budget",
				FailureClass: "input_budget", Attempts: turn - 1,
			}
		}
		result, err := p.transport.Complete(ctx, modelprovider.StructuredRequest{
			Model: p.model, Messages: append([]modelprovider.Message(nil), messages...),
			SchemaName: DreamGenerationSchemaName, Schema: DreamGenerationResponseSchema(),
			MaxInputTokens: p.limits.MaxInputTokens, MaxOutputTokens: p.limits.MaxOutputTokens,
		})
		if err != nil {
			return DreamGenerationResponse{}, err
		}
		outputTokens, err := assessor.CountTokens(result.Content, p.limits.Tokenizer)
		if err != nil {
			return DreamGenerationResponse{}, &modelprovider.ProviderError{
				Provider: "structured_dream", Message: "failed to count dream generation output tokens",
				Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
			}
		}
		turnInputTokens := inputTokens
		turnOutputTokens := outputTokens
		if result.PromptTokens > 0 {
			turnInputTokens = result.PromptTokens
		}
		if result.CompletionTokens > 0 {
			turnOutputTokens = result.CompletionTokens
		}
		inputTotal += turnInputTokens
		outputTotal += turnOutputTokens
		if result.PromptTokens > p.limits.MaxInputTokens {
			return DreamGenerationResponse{}, &modelprovider.MalformedResponseError{
				Provider: "structured_dream", Message: "dream generation provider reported input tokens beyond the configured limit",
				FailureClass: "input_budget", Attempts: turn,
			}
		}
		responseErrors := []assessor.SemanticValidationError{}
		if result.CompletionTokens > p.limits.MaxOutputTokens {
			responseErrors = append(responseErrors, errField("output_tokens", "provider reported output tokens beyond the configured limit"))
		}
		response := DreamGenerationResponse{}
		if len(responseErrors) == 0 {
			decoded, decodeErr := DecodeDreamGenerationResponseJSON([]byte(result.Content), p.limits)
			if decodeErr != nil {
				responseErrors = append(responseErrors, errField("response", "must be one complete JSON object matching the required field types"))
			} else {
				response, responseErrors = PrepareDreamGenerationResponse(prepared, decoded)
			}
		}
		if len(responseErrors) == 0 {
			response.InputTokens = inputTotal
			response.OutputTokens = outputTotal
			response.ProviderTurns = turn
			return response, nil
		}
		if turn == DreamGenerationMaxProviderTurns {
			return DreamGenerationResponse{}, &modelprovider.MalformedResponseError{
				Provider: "structured_dream", Message: "dream generation response remained invalid after bounded correction",
				FailureClass: "malformed_exhausted", Attempts: turn,
			}
		}
		correction, err := json.Marshal(map[string]any{
			"validation_errors": boundedGraphCorrectionErrors(responseErrors),
			"instruction":       dreamGenerationCorrectionInstruction,
		})
		if err != nil {
			return DreamGenerationResponse{}, &modelprovider.ProviderError{
				Provider: "structured_dream", Message: "failed to marshal dream generation correction",
				Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
			}
		}
		messages = append(messages,
			modelprovider.Message{Role: "assistant", Content: result.Content},
			modelprovider.Message{Role: "user", Content: string(correction)},
		)
	}
	return DreamGenerationResponse{}, &modelprovider.MalformedResponseError{
		Provider: "structured_dream", Message: "dream generation response remained invalid after bounded correction",
		FailureClass: "malformed_exhausted", Attempts: DreamGenerationMaxProviderTurns,
	}
}

func (p *Provider) GenerateEvidenceDiscoveries(ctx context.Context, req EvidenceDiscoveryRequest) (EvidenceDiscoveryResponse, error) {
	if p == nil || p.transport == nil {
		return EvidenceDiscoveryResponse{}, &modelprovider.ProviderError{
			Provider: "structured_dream", Message: "evidence discovery transport is unavailable",
			FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
		}
	}
	prepared, validationErrors := PrepareEvidenceDiscoveryRequest(req, p.limits)
	if len(validationErrors) > 0 {
		return EvidenceDiscoveryResponse{}, &modelprovider.ProviderError{
			Provider:     "structured_dream",
			Message:      "invalid evidence discovery request: " + joinedErrors(validationErrors),
			FailureClass: modelprovider.ProviderFailureClassRequestInvalid,
		}
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		return EvidenceDiscoveryResponse{}, &modelprovider.ProviderError{
			Provider: "structured_dream", Message: "failed to marshal evidence discovery request",
			Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
		}
	}
	messages := []modelprovider.Message{{Role: "system", Content: evidenceDiscoverySystemPrompt}, {Role: "user", Content: string(payload)}}
	inputTotal, outputTotal := 0, 0
	responseWithUsage := func(turns int) EvidenceDiscoveryResponse {
		return EvidenceDiscoveryResponse{
			InputTokens: inputTotal, OutputTokens: outputTotal, ProviderTurns: turns,
		}
	}
	for turn := 1; turn <= DreamGenerationMaxProviderTurns; turn++ {
		inputTokens, err := messageTokens(messages, p.limits.Tokenizer)
		if err != nil {
			return responseWithUsage(turn - 1), &modelprovider.ProviderError{
				Provider: "structured_dream", Message: "failed to count evidence discovery tokens",
				Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
			}
		}
		if inputTokens > p.limits.MaxInputTokens {
			return responseWithUsage(turn - 1), &modelprovider.MalformedResponseError{
				Provider: "structured_dream", Message: "evidence discovery exceeds input token budget",
				FailureClass: "input_budget", Attempts: turn - 1,
			}
		}
		result, err := p.transport.Complete(ctx, modelprovider.StructuredRequest{
			Model: p.model, Messages: append([]modelprovider.Message(nil), messages...),
			SchemaName: EvidenceDiscoverySchemaName, Schema: EvidenceDiscoveryResponseSchema(),
			MaxInputTokens: p.limits.MaxInputTokens, MaxOutputTokens: p.limits.MaxOutputTokens,
		})
		if err != nil {
			return responseWithUsage(turn), err
		}
		outputTokens, err := assessor.CountTokens(result.Content, p.limits.Tokenizer)
		if err != nil {
			return responseWithUsage(turn), &modelprovider.ProviderError{
				Provider: "structured_dream", Message: "failed to count evidence discovery output tokens",
				Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
			}
		}
		turnInputTokens := inputTokens
		turnOutputTokens := outputTokens
		if result.PromptTokens > 0 {
			turnInputTokens = result.PromptTokens
		}
		if result.CompletionTokens > 0 {
			turnOutputTokens = result.CompletionTokens
		}
		inputTotal += turnInputTokens
		outputTotal += turnOutputTokens
		if result.PromptTokens > p.limits.MaxInputTokens {
			return responseWithUsage(turn), &modelprovider.MalformedResponseError{
				Provider: "structured_dream", Message: "evidence discovery provider reported input tokens beyond the configured limit",
				FailureClass: "input_budget", Attempts: turn,
			}
		}
		responseErrors := []assessor.SemanticValidationError{}
		if result.CompletionTokens > p.limits.MaxOutputTokens {
			responseErrors = append(responseErrors, errField("output_tokens", "provider reported output tokens beyond the configured limit"))
		}
		response := EvidenceDiscoveryResponse{}
		if len(responseErrors) == 0 {
			decoded, decodeErr := DecodeEvidenceDiscoveryResponseJSON([]byte(result.Content), p.limits)
			if decodeErr != nil {
				responseErrors = append(responseErrors, errField("response", "must be one complete JSON object matching the required field types"))
			} else {
				response, responseErrors = PrepareEvidenceDiscoveryResponse(prepared, decoded)
			}
		}
		if len(responseErrors) == 0 {
			response.InputTokens = inputTotal
			response.OutputTokens = outputTotal
			response.ProviderTurns = turn
			return response, nil
		}
		if turn == DreamGenerationMaxProviderTurns {
			return responseWithUsage(turn), &modelprovider.MalformedResponseError{
				Provider: "structured_dream", Message: "evidence discovery response remained invalid after bounded correction",
				FailureClass: "malformed_exhausted", Attempts: turn,
			}
		}
		correction, err := json.Marshal(map[string]any{
			"validation_errors": boundedCorrectionErrors(responseErrors),
			"instruction":       evidenceDiscoveryCorrectionInstruction,
		})
		if err != nil {
			return responseWithUsage(turn), &modelprovider.ProviderError{
				Provider: "structured_dream", Message: "failed to marshal evidence discovery correction",
				Cause: err, FailureClass: modelprovider.ProviderFailureClassProviderUnavailable,
			}
		}
		messages = append(messages,
			modelprovider.Message{Role: "assistant", Content: result.Content},
			modelprovider.Message{Role: "user", Content: string(correction)},
		)
	}
	return responseWithUsage(DreamGenerationMaxProviderTurns), &modelprovider.MalformedResponseError{
		Provider: "structured_dream", Message: "evidence discovery response remained invalid after bounded correction",
		FailureClass: "malformed_exhausted", Attempts: DreamGenerationMaxProviderTurns,
	}
}

func messageTokens(messages []modelprovider.Message, tokenizer string) (int, error) {
	return graphMessageTokens(messages, tokenizer)
}

// graphMessageTokens preserves the daily graph Dream accounting contract. The
// old verifier measured the serialized chat-message array, including JSON
// delimiters and field names, before admitting a provider call.
func graphMessageTokens(messages []modelprovider.Message, tokenizer string) (int, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return 0, err
	}
	return assessor.CountTokens(string(encoded), tokenizer)
}

func boundedCorrectionErrors(errs []assessor.SemanticValidationError) []map[string]string {
	const maxErrors = 32
	out := make([]map[string]string, 0, min(len(errs), maxErrors))
	for _, item := range errs {
		if len(out) >= maxErrors {
			break
		}
		field := item.Field
		message := item.Message
		if len(field) > 128 {
			field = field[:128]
		}
		if len(message) > 256 {
			message = message[:256]
		}
		out = append(out, map[string]string{"field": field, "message": message})
	}
	return out
}

func boundedGraphCorrectionErrors(errs []assessor.SemanticValidationError) []assessor.SemanticValidationError {
	bounded := append([]assessor.SemanticValidationError(nil), errs...)
	sort.Slice(bounded, func(i, j int) bool {
		if bounded[i].Field == bounded[j].Field {
			return bounded[i].Message < bounded[j].Message
		}
		return bounded[i].Field < bounded[j].Field
	})
	if len(bounded) <= assessor.SemanticAssessmentMaxCorrectionErrors {
		return bounded
	}
	bounded = bounded[:assessor.SemanticAssessmentMaxCorrectionErrors-1]
	return append(bounded, assessor.SemanticValidationError{
		Field:   "response",
		Message: "additional validation errors were omitted; return one complete response matching the schema",
	})
}
