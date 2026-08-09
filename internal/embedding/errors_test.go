package embedding

import (
	"context"
	"errors"
	"testing"
	"time"
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
		{"timeout wrapper", &TimeoutError{}, "transient", "provider_timeout"},
		{"network timeout", embeddingNetworkError{timeout: true}, "transient", "provider_timeout"},
		{"network error", embeddingNetworkError{}, "transient", "provider_network_error"},
		{"rate limit", &RateLimitError{RetryAfter: 2}, "transient", "provider_rate_limited"},
		{"quota", &ProviderHTTPError{Status: 429, Code: "insufficient_quota"}, "provider_action_required", "provider_quota_exhausted"},
		{"rate limited http", &ProviderHTTPError{Status: 429, RetryAfter: 10 * time.Minute}, "transient", "provider_rate_limited"},
		{"server", &ProviderHTTPError{Status: 503}, "transient", "provider_server_error"},
		{"auth", &ProviderHTTPError{Status: 401}, "provider_action_required", "provider_authentication_failed"},
		{"permission", &ProviderHTTPError{Status: 403}, "provider_action_required", "provider_permission_denied"},
		{"contract", &ProviderHTTPError{Status: 422}, "provider_action_required", "provider_contract_rejected"},
		{"classified provider", &ProviderError{FailureClass: "transient", FailureCode: "provider_timeout", StatusCode: 504}, "transient", "provider_timeout"},
		{"unclassified provider", &ProviderError{}, "permanent", "unknown_embedding_failure"},
		{"sentinel rate limit", ErrEmbeddingRateLimit, "transient", "provider_rate_limited"},
		{"unknown", errors.New("unknown"), "permanent", "unknown_embedding_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyFailure(test.err)
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
		{0, 0}, {-time.Second, 0}, {2 * time.Second, 2 * time.Second}, {10 * time.Minute, maxProviderRetryAfter},
	} {
		if got := boundedRetryAfter(test.input); got != test.want {
			t.Fatalf("boundedRetryAfter(%s) = %s, want %s", test.input, got, test.want)
		}
	}
}
