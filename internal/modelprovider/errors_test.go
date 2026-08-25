package modelprovider

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTypedProviderErrorsExposeStableSentinels(t *testing.T) {
	cause := errors.New("transport")
	provider := &ProviderError{Provider: "openai", Message: "failed", Cause: cause}
	assert.ErrorIs(t, provider, ErrVerifierProvider)
	assert.ErrorIs(t, provider, cause)
	assert.Equal(t, "verifier provider error: openai: failed: transport", provider.Error())
	assert.Nil(t, (&ProviderError{Provider: "openai", Message: "failed"}).Unwrap())

	timeout := &TimeoutError{Provider: "openai", Message: "deadline"}
	assert.ErrorIs(t, timeout, ErrVerifierTimeout)
	assert.Equal(t, "verifier request timed out: openai: deadline", timeout.Error())

	rateLimit := &RateLimitError{Provider: "openai", Message: "busy", RetryAfter: 2}
	assert.ErrorIs(t, rateLimit, ErrVerifierRateLimit)
	assert.Equal(t, "verifier request rate limited: openai: busy", rateLimit.Error())

	malformed := &MalformedResponseError{Provider: "openai", Message: "invalid"}
	assert.ErrorIs(t, malformed, ErrVerifierMalformedResponse)
	assert.Equal(t, "verifier malformed response: openai: invalid", malformed.Error())
}

func TestProviderFailureDetailsBoundsRetryMetadata(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ProviderFailureMetadata
	}{
		{name: "retry after", err: &RateLimitError{RetryAfter: 75}, want: ProviderFailureMetadata{Class: ProviderFailureClassRateLimited, StatusCode: 429, RetryAfter: 75 * time.Second}},
		{name: "retry cap", err: &RateLimitError{RetryAfter: 3600}, want: ProviderFailureMetadata{Class: ProviderFailureClassRateLimited, StatusCode: 429, RetryAfter: 5 * time.Minute}},
		{name: "no retry", err: &RateLimitError{RetryAfter: -1}, want: ProviderFailureMetadata{Class: ProviderFailureClassRateLimited, StatusCode: 429}},
		{name: "timeout", err: &TimeoutError{}, want: ProviderFailureMetadata{Class: ProviderFailureClassTimeout}},
		{name: "provider", err: &ProviderError{FailureClass: ProviderFailureClassHTTPServer, StatusCode: 503}, want: ProviderFailureMetadata{Class: ProviderFailureClassHTTPServer, StatusCode: 503}},
		{name: "provider default", err: &ProviderError{}, want: ProviderFailureMetadata{Class: ProviderFailureClassProviderUnavailable}},
		{name: "unknown", err: errors.New("provider failed"), want: ProviderFailureMetadata{Class: ProviderFailureClassProviderUnavailable}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ProviderFailureDetails(test.err))
		})
	}
}
