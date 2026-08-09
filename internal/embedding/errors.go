package embedding

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrEmbeddingTimeout is returned when an embedding request times out.
var ErrEmbeddingTimeout = errors.New("embedding request timed out")

// ErrEmbeddingRateLimit is returned when the embedding provider rate limits the request.
var ErrEmbeddingRateLimit = errors.New("embedding request rate limited")

// ErrEmbeddingProvider is returned when the embedding provider encounters an error.
var ErrEmbeddingProvider = errors.New("embedding provider error")

// TimeoutError wraps ErrEmbeddingTimeout with additional context.
type TimeoutError struct {
	Provider string
	Message  string
}

// Error implements the error interface.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrEmbeddingTimeout, e.Provider, e.Message)
}

// Is allows errors.Is to match ErrEmbeddingTimeout.
func (e *TimeoutError) Is(target error) bool {
	return target == ErrEmbeddingTimeout
}

// RateLimitError wraps ErrEmbeddingRateLimit with additional context.
type RateLimitError struct {
	Provider   string
	Message    string
	RetryAfter int
}

// Error implements the error interface.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrEmbeddingRateLimit, e.Provider, e.Message)
}

// Is allows errors.Is to match ErrEmbeddingRateLimit.
func (e *RateLimitError) Is(target error) bool {
	return target == ErrEmbeddingRateLimit
}

// ProviderError wraps ErrEmbeddingProvider with additional context.
type ProviderError struct {
	Provider     string
	Message      string
	Cause        error
	FailureCode  string
	FailureClass string
	StatusCode   int
}

// Error implements the error interface.
func (e *ProviderError) Error() string {
	if e.Cause != nil && e.FailureCode == "" {
		return fmt.Sprintf("%s: %s: %s: %v", ErrEmbeddingProvider, e.Provider, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s: %s", ErrEmbeddingProvider, e.Provider, e.Message)
}

// Is allows errors.Is to match ErrEmbeddingProvider.
func (e *ProviderError) Is(target error) bool {
	return target == ErrEmbeddingProvider
}

// Unwrap returns the underlying cause.
func (e *ProviderError) Unwrap() error {
	return e.Cause
}

// ProviderHTTPError represents an HTTP error from an embedding provider.
// It captures the HTTP status code and message for proper error handling
// and retry classification.
type ProviderHTTPError struct {
	Status int
	// Message is server-authored context only. Provider response messages and
	// response bodies are discarded at the HTTP adapter boundary.
	Message    string
	Code       string
	Type       string
	Body       string
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *ProviderHTTPError) Error() string {
	parts := fmt.Sprintf("embedding provider http error: status=%d", e.Status)
	if e.Code != "" {
		parts += " code=" + e.Code
	}
	if e.Type != "" {
		parts += " type=" + e.Type
	}
	return parts
}

// FailureMetadata is the closed, bounded provider outcome used by job policy.
// It deliberately excludes provider messages, response bodies, and endpoints.
type FailureMetadata struct {
	Class      string
	Code       string
	StatusCode int
	RetryAfter time.Duration
}

const maxProviderRetryAfter = 5 * time.Minute

// ClassifyFailure converts an embedding error into the closed recovery
// contract. Unknown errors fail closed as permanent unknown failures.
func ClassifyFailure(err error) FailureMetadata {
	if err == nil {
		return FailureMetadata{}
	}
	if errors.Is(err, context.Canceled) {
		return FailureMetadata{Class: "transient", Code: "provider_network_error"}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrEmbeddingTimeout) {
		return FailureMetadata{Class: "transient", Code: "provider_timeout"}
	}
	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		return FailureMetadata{Class: "transient", Code: "provider_timeout"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return FailureMetadata{Class: "transient", Code: "provider_timeout"}
		}
		return FailureMetadata{Class: "transient", Code: "provider_network_error"}
	}
	var rateErr *RateLimitError
	if errors.As(err, &rateErr) {
		return FailureMetadata{Class: "transient", Code: "provider_rate_limited", StatusCode: 429, RetryAfter: boundedRetryAfter(time.Duration(rateErr.RetryAfter) * time.Second)}
	}
	var httpErr *ProviderHTTPError
	if errors.As(err, &httpErr) {
		code := normalizeProviderCode(httpErr.Code, httpErr.Type)
		if httpErr.Status == 429 {
			if code == "insufficient_quota" || code == "quota_exhausted" || code == "quota" {
				return FailureMetadata{Class: "provider_action_required", Code: "provider_quota_exhausted", StatusCode: httpErr.Status}
			}
			return FailureMetadata{Class: "transient", Code: "provider_rate_limited", StatusCode: httpErr.Status, RetryAfter: boundedRetryAfter(httpErr.RetryAfter)}
		}
		if httpErr.Status >= 500 {
			return FailureMetadata{Class: "transient", Code: "provider_server_error", StatusCode: httpErr.Status}
		}
		switch httpErr.Status {
		case 401:
			return FailureMetadata{Class: "provider_action_required", Code: "provider_authentication_failed", StatusCode: httpErr.Status}
		case 403:
			return FailureMetadata{Class: "provider_action_required", Code: "provider_permission_denied", StatusCode: httpErr.Status}
		default:
			return FailureMetadata{Class: "provider_action_required", Code: "provider_contract_rejected", StatusCode: httpErr.Status}
		}
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		if providerErr.FailureCode != "" && providerErr.FailureClass != "" {
			return FailureMetadata{Class: providerErr.FailureClass, Code: providerErr.FailureCode, StatusCode: providerErr.StatusCode}
		}
		return FailureMetadata{Class: "permanent", Code: "unknown_embedding_failure", StatusCode: providerErr.StatusCode}
	}
	if errors.Is(err, ErrEmbeddingRateLimit) {
		return FailureMetadata{Class: "transient", Code: "provider_rate_limited"}
	}
	return FailureMetadata{Class: "permanent", Code: "unknown_embedding_failure"}
}

func normalizeProviderCode(code, typ string) string {
	if code != "" {
		return code
	}
	return typ
}

func boundedRetryAfter(value time.Duration) time.Duration {
	if value <= 0 {
		return 0
	}
	if value > maxProviderRetryAfter {
		return maxProviderRetryAfter
	}
	return value
}
