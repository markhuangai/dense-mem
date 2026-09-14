package embedding

import (
	"context"
	"errors"
	"testing"
	"time"

	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
)

type embeddingNetworkError struct{ timeout bool }

func (e embeddingNetworkError) Error() string   { return "network" }
func (e embeddingNetworkError) Timeout() bool   { return e.timeout }
func (e embeddingNetworkError) Temporary() bool { return true }

func TestClassifyFailureUsesClosedProviderContract(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class string
		code  string
	}{
		{"canceled", context.Canceled, "transient", "provider_network_error"},
		{"deadline", context.DeadlineExceeded, "transient", "provider_timeout"},
		{"timeout wrapper", &embeddingcontract.TimeoutError{}, "transient", "provider_timeout"},
		{"network timeout", embeddingNetworkError{timeout: true}, "transient", "provider_timeout"},
		{"network error", embeddingNetworkError{}, "transient", "provider_network_error"},
		{"rate limit", &embeddingcontract.RateLimitError{RetryAfter: 2}, "transient", "provider_rate_limited"},
		{"quota", &embeddingcontract.ProviderHTTPError{Status: 429, Code: "insufficient_quota"}, "provider_action_required", "provider_quota_exhausted"},
		{"normalized quota", &embeddingcontract.ProviderHTTPError{Status: 429, Code: "Insufficient-Quota"}, "provider_action_required", "provider_quota_exhausted"},
		{"rate limited http", &embeddingcontract.ProviderHTTPError{Status: 429, RetryAfter: 10 * time.Minute}, "transient", "provider_rate_limited"},
		{"request timeout http", &embeddingcontract.ProviderHTTPError{Status: 408}, "transient", "provider_timeout"},
		{"server", &embeddingcontract.ProviderHTTPError{Status: 503}, "transient", "provider_server_error"},
		{"payload too large", &embeddingcontract.ProviderHTTPError{Status: 413}, "permanent", "embedding_input_rejected"},
		{"context length", &embeddingcontract.ProviderHTTPError{Status: 400, Code: "context_length_exceeded"}, "permanent", "embedding_input_rejected"},
		{"generic bad request", &embeddingcontract.ProviderHTTPError{Status: 400, Code: "invalid_request_error"}, "provider_action_required", "provider_contract_rejected"},
		{"auth", &embeddingcontract.ProviderHTTPError{Status: 401}, "provider_action_required", "provider_authentication_failed"},
		{"permission", &embeddingcontract.ProviderHTTPError{Status: 403}, "provider_action_required", "provider_permission_denied"},
		{"contract", &embeddingcontract.ProviderHTTPError{Status: 422}, "provider_action_required", "provider_contract_rejected"},
		{"classified provider", &embeddingcontract.ProviderError{FailureClass: "transient", FailureCode: "provider_timeout", StatusCode: 504}, "transient", "provider_timeout"},
		{"unsupported provider class", &embeddingcontract.ProviderError{FailureClass: "retry", FailureCode: "provider_timeout", StatusCode: 504}, "permanent", "unknown_embedding_failure"},
		{"mismatched provider contract", &embeddingcontract.ProviderError{FailureClass: "permanent", FailureCode: "provider_timeout", StatusCode: 504}, "permanent", "unknown_embedding_failure"},
		{"unclassified provider", &embeddingcontract.ProviderError{}, "permanent", "unknown_embedding_failure"},
		{"sentinel rate limit", embeddingcontract.ErrEmbeddingRateLimit, "transient", "provider_rate_limited"},
		{"unknown", errors.New("unknown"), "permanent", "unknown_embedding_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := embeddingcontract.ClassifyFailure(test.err)
			if got.Class != test.class || got.Code != test.code {
				t.Fatalf("ClassifyFailure = %#v, want class=%q code=%q", got, test.class, test.code)
			}
		})
	}
}

func TestBoundedRetryAfterClampsProviderHints(t *testing.T) {
	for _, test := range []struct {
		input, want time.Duration
	}{
		{0, 0}, {-time.Second, 0}, {2 * time.Second, 2 * time.Second}, {10 * time.Minute, embeddingcontract.MaxProviderRetryAfter},
	} {
		if got := embeddingcontract.BoundedRetryAfter(test.input); got != test.want {
			t.Fatalf("boundedRetryAfter(%s) = %s, want %s", test.input, got, test.want)
		}
	}
}
