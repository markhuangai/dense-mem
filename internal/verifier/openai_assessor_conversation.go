package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func semanticAssessmentMessageTokens(messages []openAIVerifierMessage, tokenizerName string) (int, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return 0, err
	}
	return CountTokens(string(encoded), tokenizerName)
}

func boundedSemanticAssessmentCorrectionErrors(errs []SemanticValidationError) []SemanticValidationError {
	bounded := append([]SemanticValidationError(nil), errs...)
	sort.Slice(bounded, func(i, j int) bool {
		if bounded[i].Field == bounded[j].Field {
			return bounded[i].Message < bounded[j].Message
		}
		return bounded[i].Field < bounded[j].Field
	})
	if len(bounded) <= SemanticAssessmentMaxCorrectionErrors {
		return bounded
	}
	bounded = bounded[:SemanticAssessmentMaxCorrectionErrors-1]
	return append(bounded, SemanticValidationError{
		Field:   "response",
		Message: "additional validation errors were omitted; return one complete response matching the schema",
	})
}

func semanticAssessmentResponseForCorrection(
	req SemanticAssessmentRequest,
	result openAIStructuredChatResult,
	limits SemanticAssessmentLimits,
) (SemanticAssessmentResponse, []SemanticValidationError, string) {
	if result.Usage != nil && result.Usage.CompletionTokens > int64(limits.MaxOutputTokens) {
		return SemanticAssessmentResponse{}, []SemanticValidationError{semanticErr(
			"output_tokens",
			fmt.Sprintf("provider reported more than the allowed %d tokens", limits.MaxOutputTokens),
		)}, "response_output_tokens"
	}
	outputTokens, err := CountTokens(result.Content, limits.Tokenizer)
	if err != nil {
		return SemanticAssessmentResponse{}, []SemanticValidationError{
			semanticErr("response", "could not be token-counted"),
		}, "response_json"
	}
	if outputTokens > limits.MaxOutputTokens {
		return SemanticAssessmentResponse{}, []SemanticValidationError{semanticErr(
			"output_tokens",
			fmt.Sprintf("must be less than or equal to %d", limits.MaxOutputTokens),
		)}, "response_output_tokens"
	}
	if validationErrors := validateSemanticAssessmentResponseRaw([]byte(result.Content)); len(validationErrors) > 0 {
		return SemanticAssessmentResponse{}, validationErrors, "response_json"
	}
	response, err := DecodeSemanticAssessmentResponseJSON([]byte(result.Content), limits)
	if err != nil {
		field := "response"
		message := "must be one complete JSON object matching the required field types"
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) && typeError.Field != "" {
			field = typeError.Field
			message = "must match the required JSON type"
		}
		return SemanticAssessmentResponse{}, []SemanticValidationError{
			semanticErr(field, message),
		}, "response_json"
	}
	normalized, validationErrors := PrepareSemanticAssessmentResponse(req, response, limits)
	if len(validationErrors) > 0 {
		return SemanticAssessmentResponse{}, validationErrors, "response_contract"
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
