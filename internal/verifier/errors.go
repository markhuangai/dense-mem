package verifier

import (
	"errors"
	"fmt"
	"time"
)

const (
	ProviderFailureClassTimeout             = "timeout"
	ProviderFailureClassRateLimited         = "rate_limited"
	ProviderFailureClassHTTPClient          = "http_4xx"
	ProviderFailureClassHTTPServer          = "http_5xx"
	ProviderFailureClassHTTPUnexpected      = "http_unexpected"
	ProviderFailureClassTransport           = "transport"
	ProviderFailureClassProtocol            = "provider_protocol"
	ProviderFailureClassRequestInvalid      = "request_invalid"
	ProviderFailureClassProviderUnavailable = "provider_unavailable"

	providerFailureMaxRetryAfter = 5 * time.Minute
)

// ErrVerifierTimeout is returned when a verifier request times out.
var ErrVerifierTimeout = errors.New("verifier request timed out")

// ErrVerifierProvider is returned when the verifier provider encounters an error.
var ErrVerifierProvider = errors.New("verifier provider error")

// ErrVerifierRateLimit is returned when the verifier provider rate limits the request.
var ErrVerifierRateLimit = errors.New("verifier request rate limited")

// ErrVerifierMalformedResponse is returned when the verifier provider returns a response
// that cannot be parsed or does not conform to the expected schema.
var ErrVerifierMalformedResponse = errors.New("verifier malformed response")

// TimeoutError wraps ErrVerifierTimeout with additional context.
type TimeoutError struct {
	Provider string
	Message  string
}

// Error implements the error interface.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrVerifierTimeout, e.Provider, e.Message)
}

// Is allows errors.Is to match ErrVerifierTimeout.
func (e *TimeoutError) Is(target error) bool {
	return target == ErrVerifierTimeout
}

// ProviderError wraps ErrVerifierProvider with additional context.
type ProviderError struct {
	Provider     string
	Message      string
	Cause        error
	FailureClass string
	StatusCode   int
}

// Error implements the error interface.
func (e *ProviderError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %s: %v", ErrVerifierProvider, e.Provider, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s: %s", ErrVerifierProvider, e.Provider, e.Message)
}

// Is allows errors.Is to match ErrVerifierProvider.
func (e *ProviderError) Is(target error) bool {
	return target == ErrVerifierProvider
}

// Unwrap returns the underlying cause.
func (e *ProviderError) Unwrap() error {
	return e.Cause
}

// RateLimitError wraps ErrVerifierRateLimit with additional context.
type RateLimitError struct {
	Provider   string
	Message    string
	RetryAfter int
}

// Error implements the error interface.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrVerifierRateLimit, e.Provider, e.Message)
}

// Is allows errors.Is to match ErrVerifierRateLimit.
func (e *RateLimitError) Is(target error) bool {
	return target == ErrVerifierRateLimit
}

// MalformedResponseError wraps ErrVerifierMalformedResponse with additional context.
type MalformedResponseError struct {
	Provider                string
	Message                 string
	RawJSON                 string
	FailureClass            string
	Attempts                int
	ValidationStage         string
	ValidationFieldFamilies []string
	Measurement             *FailureMeasurement
}

// FailureMeasurement is bounded numeric context for a deterministic
// validation failure. It never contains provider or evidence content.
type FailureMeasurement struct {
	Unit            string
	Observed        int
	ObservedAtLeast bool
	Limit           int
}

// Error implements the error interface.
func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrVerifierMalformedResponse, e.Provider, e.Message)
}

// Is allows errors.Is to match ErrVerifierMalformedResponse.
func (e *MalformedResponseError) Is(target error) bool {
	return target == ErrVerifierMalformedResponse
}

// ProviderFailureMetadata is safe, bounded retry data derived from a provider error.
type ProviderFailureMetadata struct {
	Class      string
	StatusCode int
	RetryAfter time.Duration
}

// ProviderFailureDetails excludes provider messages and response bodies.
func ProviderFailureDetails(err error) ProviderFailureMetadata {
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		retryAfter := time.Duration(0)
		switch {
		case rateLimit.RetryAfter >= int(providerFailureMaxRetryAfter/time.Second):
			retryAfter = providerFailureMaxRetryAfter
		case rateLimit.RetryAfter > 0:
			retryAfter = time.Duration(rateLimit.RetryAfter) * time.Second
		}
		return ProviderFailureMetadata{
			Class:      ProviderFailureClassRateLimited,
			StatusCode: 429,
			RetryAfter: retryAfter,
		}
	}
	if errors.Is(err, ErrVerifierTimeout) {
		return ProviderFailureMetadata{Class: ProviderFailureClassTimeout}
	}
	var provider *ProviderError
	if errors.As(err, &provider) {
		class := provider.FailureClass
		if class == "" {
			class = ProviderFailureClassProviderUnavailable
		}
		return ProviderFailureMetadata{
			Class:      class,
			StatusCode: provider.StatusCode,
		}
	}
	return ProviderFailureMetadata{Class: ProviderFailureClassProviderUnavailable}
}
