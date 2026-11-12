package assessorprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func semanticAssessmentTurnTokens(
	model string,
	schemaName string,
	messages []openAIVerifierMessage,
	schema map[string]any,
	temperatureDisabled bool,
	tokenizerName string,
) (int, error) {
	converted := make([]assessor.SemanticAssessmentProviderMessage, 0, len(messages))
	for _, message := range messages {
		converted = append(converted, assessor.SemanticAssessmentProviderMessage{Role: message.Role, Content: message.Content})
	}
	return assessor.CountSemanticAssessmentProviderRequestTokens(
		model, schemaName, schema, temperatureDisabled, converted, tokenizerName,
	)
}

// semanticAssessmentConversationCandidateContextTokens counts every
// candidate-context occurrence in the complete conversation. The initial
// request remains in history; an explicitly supplied refreshed context is
// counted too so compatibility extensions cannot bypass the sub-budget.
func semanticAssessmentConversationCandidateContextTokens(messages []openAIVerifierMessage, tokenizerName string) (int, error) {
	total := 0
	for _, message := range messages {
		if strings.ToLower(strings.TrimSpace(message.Role)) != "user" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(message.Content), &fields); err != nil {
			continue
		}
		if refreshed, ok := fields["refreshed_candidate_context"]; ok && len(refreshed) > 0 {
			count, err := assessor.CountTokens(string(refreshed), tokenizerName)
			if err != nil {
				return 0, err
			}
			total += count
		}
		candidateFields := map[string]json.RawMessage{}
		for _, key := range []string{"evidence_equivalence_candidates"} {
			if value, ok := fields[key]; ok {
				candidateFields[key] = value
			}
		}
		if len(candidateFields) == 0 {
			continue
		}
		candidateJSON, err := json.Marshal(candidateFields)
		if err != nil {
			return 0, err
		}
		count, err := assessor.CountTokens(string(candidateJSON), tokenizerName)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func semanticAssessmentValidationFieldFamilies(errs []assessor.SemanticValidationError) []string {
	seen := make(map[string]struct{}, len(errs))
	families := make([]string, 0, len(errs))
	for _, validationError := range errs {
		family := semanticAssessmentValidationFieldFamily(validationError.Field)
		if _, exists := seen[family]; exists {
			continue
		}
		seen[family] = struct{}{}
		families = append(families, family)
	}
	return families
}

func semanticAssessmentValidationFieldFamily(field string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	switch field {
	case "request_id":
		return "request_id"
	case "input_tokens":
		return "input_tokens"
	case "output_tokens":
		return "output_tokens"
	case "response":
		return "response"
	}
	switch {
	case strings.HasPrefix(field, "evidence_security_results"):
		return "evidence_security_results"
	case strings.HasPrefix(field, "evidence_equivalence_results"), strings.HasPrefix(field, "evidence_equivalence_candidates"):
		return "evidence_equivalence"
	case strings.HasPrefix(field, "evidence_conflict_results"):
		return "evidence_conflicts"
	case strings.HasPrefix(field, "security_signals"), strings.HasPrefix(field, "security_results"):
		return "evidence_security_results"
	default:
		return "other"
	}
}

func semanticAssessmentResponseForCorrection(
	req assessor.SemanticAssessmentRequest,
	result openAIStructuredChatResult,
	limits assessor.SemanticAssessmentLimits,
) (assessor.SemanticAssessmentResponse, []assessor.SemanticValidationError, string) {
	if result.ReportedUsage != nil && result.ReportedUsage.CompletionTokens > int64(limits.MaxOutputTokens) {
		return assessor.SemanticAssessmentResponse{}, []assessor.SemanticValidationError{errField(
			"output_tokens",
			fmt.Sprintf("provider reported more than the allowed %d tokens", limits.MaxOutputTokens),
		)}, "response_output_tokens"
	}
	outputTokens, err := assessor.CountTokens(result.Content, limits.Tokenizer)
	if err != nil {
		return assessor.SemanticAssessmentResponse{}, []assessor.SemanticValidationError{
			errField("response", "could not be token-counted"),
		}, "response_json"
	}
	if outputTokens > limits.MaxOutputTokens {
		return assessor.SemanticAssessmentResponse{}, []assessor.SemanticValidationError{errField(
			"output_tokens",
			fmt.Sprintf("must be less than or equal to %d", limits.MaxOutputTokens),
		)}, "response_output_tokens"
	}
	if validationErrors := assessor.ValidateSemanticAssessmentResponseRaw([]byte(result.Content)); req.DecisionRouting == nil && len(validationErrors) > 0 {
		return assessor.SemanticAssessmentResponse{}, validationErrors, "response_json"
	}
	response, err := assessor.DecodeSemanticAssessmentGeneration([]byte(result.Content), req, limits)
	if err != nil {
		var validation *assessor.SemanticAssessmentResponseValidationError
		if errors.As(err, &validation) {
			return assessor.SemanticAssessmentResponse{}, validation.Errors, "response_json"
		}
		return assessor.SemanticAssessmentResponse{}, []assessor.SemanticValidationError{assessor.ResponseJSONValidationError(err, "")}, "response_json"
	}
	normalized, validationErrors := assessor.PrepareSemanticAssessmentResponse(req, response, limits)
	if len(validationErrors) > 0 {
		return normalized, validationErrors, "response_contract"
	}
	return normalized, nil, ""
}

func openAIHTTPFailureClass(statusCode int) string {
	switch {
	case statusCode >= 400 && statusCode < 500:
		return ProviderFailureClassHTTPClient
	case statusCode >= 500 && statusCode < 600:
		return ProviderFailureClassHTTPServer
	default:
		return ProviderFailureClassHTTPUnexpected
	}
}

func openAIRetryAfterSeconds(value string, now time.Time) int {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds >= int(providerFailureMaxRetryAfter/time.Second) {
			return int(providerFailureMaxRetryAfter / time.Second)
		}
		if seconds > 0 {
			return seconds
		}
		return 0
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := retryAt.Sub(now)
	if delay <= 0 {
		return 0
	}
	seconds := int((delay + time.Second - 1) / time.Second)
	if seconds >= int(providerFailureMaxRetryAfter/time.Second) {
		return int(providerFailureMaxRetryAfter / time.Second)
	}
	return seconds
}

func openAIRequestTimedOutOrCanceled(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

func errField(field, message string) assessor.SemanticValidationError {
	return assessor.SemanticValidationError{Field: field, Message: message}
}
