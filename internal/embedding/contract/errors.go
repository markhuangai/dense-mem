package contract

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var ErrEmbeddingTimeout = errors.New("embedding request timed out")
var ErrEmbeddingRateLimit = errors.New("embedding request rate limited")
var ErrEmbeddingProvider = errors.New("embedding provider error")

type TimeoutError struct {
	Provider string
	Message  string
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrEmbeddingTimeout, e.Provider, e.Message)
}

func (e *TimeoutError) Is(target error) bool { return target == ErrEmbeddingTimeout }

type RateLimitError struct {
	Provider   string
	Message    string
	RetryAfter int
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrEmbeddingRateLimit, e.Provider, e.Message)
}

func (e *RateLimitError) Is(target error) bool { return target == ErrEmbeddingRateLimit }

type ProviderError struct {
	Provider     string
	Message      string
	Cause        error
	FailureCode  string
	FailureClass string
	StatusCode   int
}

func (e *ProviderError) Error() string {
	if e.Cause != nil && e.FailureCode == "" {
		return fmt.Sprintf("%s: %s: %s: %v", ErrEmbeddingProvider, e.Provider, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s: %s", ErrEmbeddingProvider, e.Provider, e.Message)
}

func (e *ProviderError) Is(target error) bool { return target == ErrEmbeddingProvider }
func (e *ProviderError) Unwrap() error        { return e.Cause }

type ProviderHTTPError struct {
	Status     int
	Message    string
	Code       string
	Type       string
	RetryAfter time.Duration
}

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

type FailureMetadata struct {
	Class      string
	Code       string
	StatusCode int
	RetryAfter time.Duration
}

const MaxProviderRetryAfter = 5 * time.Minute

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
		return FailureMetadata{Class: "transient", Code: "provider_rate_limited", StatusCode: 429, RetryAfter: BoundedRetryAfter(time.Duration(rateErr.RetryAfter) * time.Second)}
	}
	var httpErr *ProviderHTTPError
	if errors.As(err, &httpErr) {
		code := normalizeProviderCode(httpErr.Code, httpErr.Type)
		if httpErr.Status == 408 {
			return FailureMetadata{Class: "transient", Code: "provider_timeout", StatusCode: httpErr.Status}
		}
		if httpErr.Status == 429 {
			if code == "insufficient_quota" || code == "quota_exhausted" || code == "quota" {
				return FailureMetadata{Class: "provider_action_required", Code: "provider_quota_exhausted", StatusCode: httpErr.Status}
			}
			return FailureMetadata{Class: "transient", Code: "provider_rate_limited", StatusCode: httpErr.Status, RetryAfter: BoundedRetryAfter(httpErr.RetryAfter)}
		}
		if httpErr.Status >= 500 {
			return FailureMetadata{Class: "transient", Code: "provider_server_error", StatusCode: httpErr.Status}
		}
		if isInputRejection(httpErr.Status, code) {
			return FailureMetadata{Class: "permanent", Code: "embedding_input_rejected", StatusCode: httpErr.Status}
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
		if domain.EmbeddingFailureContractValid(providerErr.FailureClass, providerErr.FailureCode) {
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
	if strings.TrimSpace(code) != "" {
		return normalizeFailureToken(code)
	}
	return normalizeFailureToken(typ)
}

func normalizeFailureToken(value string) string {
	return strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(strings.TrimSpace(value)))
}

func isInputRejection(status int, code string) bool {
	if status == 413 {
		return true
	}
	if status != 400 {
		return false
	}
	switch normalizeFailureToken(code) {
	case "context_length_exceeded", "input_too_large", "input_too_long", "max_input_tokens", "payload_too_large", "content_too_large", "string_above_max_length":
		return true
	default:
		return false
	}
}

func BoundedRetryAfter(value time.Duration) time.Duration {
	if value <= 0 {
		return 0
	}
	if value > MaxProviderRetryAfter {
		return MaxProviderRetryAfter
	}
	return value
}
