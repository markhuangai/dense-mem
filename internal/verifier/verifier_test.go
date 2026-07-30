package verifier

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubVerifier is a test-local implementation of Verifier used to exercise
// the interface without an external provider.
type stubVerifier struct {
	fn func(ctx context.Context, req Request) (Response, error)
}

func (s *stubVerifier) Verify(ctx context.Context, req Request) (Response, error) {
	return s.fn(ctx, req)
}

// Compile-time assertion: stubVerifier satisfies Verifier.
var _ Verifier = (*stubVerifier)(nil)

// TestSentinels verifies that each exported sentinel error is distinct and
// that the typed error wrappers satisfy errors.Is correctly.
func TestSentinels(t *testing.T) {
	t.Run("ErrVerifierTimeout", func(t *testing.T) {
		wrapped := &TimeoutError{Provider: "openai", Message: "deadline exceeded"}
		assert.ErrorIs(t, wrapped, ErrVerifierTimeout)
		assert.Equal(t, "verifier request timed out: openai: deadline exceeded", wrapped.Error())
		assert.NotErrorIs(t, wrapped, ErrVerifierProvider)
		assert.NotErrorIs(t, wrapped, ErrVerifierRateLimit)
		assert.NotErrorIs(t, wrapped, ErrVerifierMalformedResponse)
	})

	t.Run("ErrVerifierProvider", func(t *testing.T) {
		wrapped := &ProviderError{Provider: "openai", Message: "500 internal server error"}
		assert.ErrorIs(t, wrapped, ErrVerifierProvider)
		assert.Equal(t, "verifier provider error: openai: 500 internal server error", wrapped.Error())
		assert.NoError(t, errors.Unwrap(wrapped))
		assert.NotErrorIs(t, wrapped, ErrVerifierTimeout)
		assert.NotErrorIs(t, wrapped, ErrVerifierRateLimit)
		assert.NotErrorIs(t, wrapped, ErrVerifierMalformedResponse)
	})

	t.Run("ErrVerifierProvider_WithCause", func(t *testing.T) {
		cause := errors.New("tcp: connection reset")
		wrapped := &ProviderError{Provider: "openai", Message: "transport error", Cause: cause}
		assert.ErrorIs(t, wrapped, ErrVerifierProvider)
		assert.ErrorIs(t, wrapped, cause)
		assert.Equal(t, "verifier provider error: openai: transport error: tcp: connection reset", wrapped.Error())
		assert.ErrorIs(t, errors.Unwrap(wrapped), cause)
	})

	t.Run("ErrVerifierRateLimit", func(t *testing.T) {
		wrapped := &RateLimitError{Provider: "openai", Message: "too many requests", RetryAfter: 60}
		assert.ErrorIs(t, wrapped, ErrVerifierRateLimit)
		assert.Equal(t, "verifier request rate limited: openai: too many requests", wrapped.Error())
		assert.NotErrorIs(t, wrapped, ErrVerifierTimeout)
		assert.NotErrorIs(t, wrapped, ErrVerifierProvider)
		assert.NotErrorIs(t, wrapped, ErrVerifierMalformedResponse)
	})

	t.Run("ErrVerifierMalformedResponse", func(t *testing.T) {
		wrapped := &MalformedResponseError{Provider: "openai", Message: "unexpected field", RawJSON: `{"x":1}`}
		assert.ErrorIs(t, wrapped, ErrVerifierMalformedResponse)
		assert.Equal(t, "verifier malformed response: openai: unexpected field", wrapped.Error())
		assert.NotErrorIs(t, wrapped, ErrVerifierTimeout)
		assert.NotErrorIs(t, wrapped, ErrVerifierProvider)
		assert.NotErrorIs(t, wrapped, ErrVerifierRateLimit)
		// RawJSON is preserved on the struct
		assert.Equal(t, `{"x":1}`, wrapped.RawJSON)
	})

	t.Run("SentinelsAreDistinct", func(t *testing.T) {
		sentinels := []error{
			ErrVerifierTimeout,
			ErrVerifierProvider,
			ErrVerifierRateLimit,
			ErrVerifierMalformedResponse,
		}
		for i, a := range sentinels {
			for j, b := range sentinels {
				if i != j {
					assert.NotEqual(t, a, b, "sentinels at index %d and %d must be distinct", i, j)
				}
			}
		}
	})
}

func TestProviderFailureDetailsReturnsBoundedRetryMetadata(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ProviderFailureMetadata
	}{
		{
			name: "rate limit",
			err:  &RateLimitError{Provider: "openai", Message: "HTTP 429", RetryAfter: 75},
			want: ProviderFailureMetadata{Class: ProviderFailureClassRateLimited, StatusCode: 429, RetryAfter: 75 * time.Second},
		},
		{
			name: "rate limit capped before duration conversion",
			err:  &RateLimitError{Provider: "openai", Message: "HTTP 429", RetryAfter: int(^uint(0) >> 1)},
			want: ProviderFailureMetadata{Class: ProviderFailureClassRateLimited, StatusCode: 429, RetryAfter: 5 * time.Minute},
		},
		{
			name: "rate limit without retry hint",
			err:  &RateLimitError{Provider: "openai", Message: "HTTP 429", RetryAfter: -1},
			want: ProviderFailureMetadata{Class: ProviderFailureClassRateLimited, StatusCode: 429},
		},
		{
			name: "timeout",
			err:  &TimeoutError{Provider: "openai", Message: "deadline"},
			want: ProviderFailureMetadata{Class: ProviderFailureClassTimeout},
		},
		{
			name: "http status",
			err: &ProviderError{
				Provider:     "openai",
				Message:      "HTTP 503",
				FailureClass: ProviderFailureClassHTTPServer,
				StatusCode:   503,
			},
			want: ProviderFailureMetadata{Class: ProviderFailureClassHTTPServer, StatusCode: 503},
		},
		{
			name: "untyped provider failure",
			err:  errors.New("provider failed"),
			want: ProviderFailureMetadata{Class: ProviderFailureClassProviderUnavailable},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, ProviderFailureDetails(test.err))
		})
	}
}

// TestVerifier exercises the Verifier interface via a stub implementation.
// It covers AC-23 (interface contract) and AC-28 (Response.RawJSON field).
func TestVerifier(t *testing.T) {
	want := Response{
		Verdict:    "supported",
		Confidence: 0.9,
		Reasoning:  "evidence aligns",
		RawJSON:    `{"verdict":"supported","confidence":0.9}`,
	}

	svc := &stubVerifier{
		fn: func(_ context.Context, req Request) (Response, error) {
			require.Equal(t, "profile-A", req.ProfileID)
			return want, nil
		},
	}

	got, err := svc.Verify(context.Background(), Request{
		ProfileID: "profile-A",
		Predicate: "The sky is blue",
		Context:   "Atmospheric scattering favours short wavelengths.",
	})

	require.NoError(t, err)
	require.Equal(t, want, got)
	// AC-28: RawJSON must be present on Response.
	assert.NotEmpty(t, got.RawJSON)
}

// TestVerifier_CrossProfileIsolation verifies that a verifier implementation
// scoped to profile A does not leak data to profile B. The stub enforces the
// profile_id boundary as required by AGENTS.md.
func TestVerifier_CrossProfileIsolation(t *testing.T) {
	const profileA = "profile-A"
	const profileB = "profile-B"

	responseForA := Response{Verdict: "supported", RawJSON: `{"id":"A"}`}

	svc := &stubVerifier{
		fn: func(_ context.Context, req Request) (Response, error) {
			// Simulate a store that is scoped to profileA only.
			if req.ProfileID != profileA {
				return Response{}, &ProviderError{
					Provider: "stub",
					Message:  "profile not found",
				}
			}
			return responseForA, nil
		},
	}

	// Profile A retrieves its own result.
	gotA, err := svc.Verify(context.Background(), Request{ProfileID: profileA, Predicate: "claim"})
	require.NoError(t, err)
	assert.Equal(t, responseForA, gotA)

	// Profile B must not receive profile A's result.
	gotB, err := svc.Verify(context.Background(), Request{ProfileID: profileB, Predicate: "claim"})
	assert.ErrorIs(t, err, ErrVerifierProvider)
	assert.Empty(t, gotB.RawJSON)
	assert.NotContains(t, gotB.RawJSON, "A")
}
