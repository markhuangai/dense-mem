package embedding

import (
	"errors"
	"fmt"
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
	Provider string
	Message  string
	Cause    error
}

// Error implements the error interface.
func (e *ProviderError) Error() string {
	if e.Cause != nil {
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
	Status  int
	Message string
	Body    string
}

// Error implements the error interface.
func (e *ProviderHTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("embedding provider http error: status=%d message=%s", e.Status, e.Message)
	}
	return fmt.Sprintf("embedding provider http error: status=%d", e.Status)
}
