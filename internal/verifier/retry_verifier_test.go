package verifier

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
)

// stubConfigProvider satisfies config.ConfigProvider for unit tests without
// touching environment variables or the real Config loader.
type stubConfigProvider struct {
	maxConcurrency int
}

func (s *stubConfigProvider) GetPostgresDSN() string                 { return "" }
func (s *stubConfigProvider) GetRedisAddr() string                   { return "" }
func (s *stubConfigProvider) GetRedisPassword() string               { return "" }
func (s *stubConfigProvider) GetRedisDB() int                        { return 0 }
func (s *stubConfigProvider) GetHTTPMaxBodyBytes() int               { return 1048576 }
func (s *stubConfigProvider) GetRateLimitPerMinute() int             { return 100 }
func (s *stubConfigProvider) GetFragmentCreateRateLimit() int        { return 60 }
func (s *stubConfigProvider) GetFragmentReadRateLimit() int          { return 300 }
func (s *stubConfigProvider) GetSSEHeartbeatSeconds() int            { return 30 }
func (s *stubConfigProvider) GetSSEMaxDurationSeconds() int          { return 300 }
func (s *stubConfigProvider) GetSSEMaxConcurrentStreams() int        { return 10 }
func (s *stubConfigProvider) GetEmbeddingDimensions() int            { return 1536 }
func (s *stubConfigProvider) GetAIAPIURL() string                    { return "" }
func (s *stubConfigProvider) GetAIAPIKey() string                    { return "" }
func (s *stubConfigProvider) GetAIEmbeddingModel() string            { return "" }
func (s *stubConfigProvider) GetAIEmbeddingDimensions() int          { return 0 }
func (s *stubConfigProvider) GetAIEmbeddingTimeoutSeconds() int      { return 30 }
func (s *stubConfigProvider) IsEmbeddingConfigured() bool            { return false }
func (s *stubConfigProvider) GetAIVerifierAPIURL() string            { return "" }
func (s *stubConfigProvider) GetAIVerifierAPIKey() string            { return "" }
func (s *stubConfigProvider) GetAIVerifierModel() string             { return "gpt-4o-mini" }
func (s *stubConfigProvider) GetAIVerifierTimeoutSeconds() int       { return 60 }
func (s *stubConfigProvider) GetAIVerifierMaxConcurrency() int       { return s.maxConcurrency }
func (s *stubConfigProvider) GetClaimWriteRateLimit() int            { return 60 }
func (s *stubConfigProvider) GetClaimReadRateLimit() int             { return 300 }
func (s *stubConfigProvider) GetRecallValidatedClaimWeight() float64 { return 0.5 }
func (s *stubConfigProvider) GetPromoteTxTimeoutSeconds() int        { return 10 }
func (s *stubConfigProvider) GetSkillPackImportHistoryDays() int     { return 30 }
func (s *stubConfigProvider) GetAICommunityMaxNodes() int            { return 500000 }
func (s *stubConfigProvider) GetControlHTTPAddr() string             { return "127.0.0.1:8090" }
func (s *stubConfigProvider) GetControlPortalToken() string          { return "" }

// newTestCfg returns a stub config with the given concurrency cap.
func newTestCfg(concurrency int) *stubConfigProvider {
	return &stubConfigProvider{maxConcurrency: concurrency}
}

// TestRetryVerifier_SuccessFirstAttempt verifies that when the inner verifier
// succeeds immediately the result is returned without retrying.
func TestRetryVerifier(t *testing.T) {
	t.Run("SuccessFirstAttempt", func(t *testing.T) {
		want := Response{
			Verdict:    "entailed",
			Confidence: 0.95,
			Reasoning:  "strong evidence",
			RawJSON:    `{"verdict":"entailed"}`,
		}

		inner := &stubVerifier{fn: func(_ context.Context, req Request) (Response, error) {
			require.Equal(t, "profile-A", req.ProfileID)
			return want, nil
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		got, err := svc.Verify(context.Background(), Request{
			ProfileID: "profile-A",
			Predicate: "Water boils at 100°C at sea level.",
		})

		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("RetryOnProviderError_SucceedsOnThirdAttempt", func(t *testing.T) {
		var calls atomic.Int32
		want := Response{Verdict: "entailed", Confidence: 0.8, Reasoning: "ok", RawJSON: `{}`}

		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			n := calls.Add(1)
			if n < 3 {
				return Response{}, &ProviderError{Provider: "stub", Message: "server error"}
			}
			return want, nil
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		got, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})

		require.NoError(t, err)
		require.Equal(t, want, got)
		assert.Equal(t, int32(3), calls.Load(), "must have taken exactly 3 calls")
	})

	t.Run("RetryOnTimeout_SucceedsOnSecondAttempt", func(t *testing.T) {
		var calls atomic.Int32
		want := Response{Verdict: "insufficient", Confidence: 0.5, Reasoning: "partial", RawJSON: `{}`}

		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			if calls.Add(1) == 1 {
				return Response{}, &TimeoutError{Provider: "stub", Message: "deadline exceeded"}
			}
			return want, nil
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		got, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})

		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("ExhaustedRetries_ReturnsLastError_PreservesSentinel", func(t *testing.T) {
		providerErr := &ProviderError{Provider: "stub", Message: "persistent 500"}

		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			return Response{}, providerErr
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		_, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})

		require.Error(t, err)
		// Sentinel class must be preserved after the retry wrapper.
		require.True(t, errors.Is(err, ErrVerifierProvider),
			"expected ErrVerifierProvider sentinel, got %v", err)
	})

	t.Run("RateLimitError_NoRetryAfter_StillRetries", func(t *testing.T) {
		var calls atomic.Int32
		want := Response{Verdict: "contradicted", Confidence: 0.7, Reasoning: "refuted", RawJSON: `{}`}

		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			if calls.Add(1) == 1 {
				return Response{}, &RateLimitError{Provider: "stub", Message: "429", RetryAfter: 0}
			}
			return want, nil
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		got, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})

		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("ProviderErrorAfterContextCancel_NotRetried", func(t *testing.T) {
		var calls atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		providerErr := &ProviderError{Provider: "stub", Message: "server error"}
		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			calls.Add(1)
			cancel()
			return Response{}, providerErr
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		_, err := svc.Verify(ctx, Request{ProfileID: "profile-A", Predicate: "claim"})

		require.ErrorIs(t, err, ErrVerifierProvider)
		require.Equal(t, int32(1), calls.Load())
	})

	t.Run("RateLimitRetryAfterStopsWhenContextCancelled", func(t *testing.T) {
		rateErr := &RateLimitError{Provider: "stub", Message: "429", RetryAfter: 1}
		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			return Response{}, rateErr
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			time.Sleep(5 * time.Millisecond)
			cancel()
		}()

		svc := NewRetryVerifier(inner, newTestCfg(5))
		_, err := svc.Verify(ctx, Request{ProfileID: "profile-A", Predicate: "claim"})

		require.ErrorIs(t, err, ErrVerifierRateLimit)
	})

	t.Run("MalformedResponse_NotRetried", func(t *testing.T) {
		var calls atomic.Int32

		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			calls.Add(1)
			return Response{}, &MalformedResponseError{Provider: "stub", Message: "bad json"}
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		_, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrVerifierMalformedResponse))
		// Must not have retried a malformed response.
		assert.Equal(t, int32(1), calls.Load(), "malformed responses must not be retried")
	})

	t.Run("ContextCancelled_WhileWaiting_ReturnsTimeout", func(t *testing.T) {
		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			return Response{}, nil
		}}

		// Fill the semaphore so the next call must wait.
		svc := NewRetryVerifier(inner, newTestCfg(1))
		// Occupy the single slot.
		svc.sem <- struct{}{}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := svc.Verify(ctx, Request{ProfileID: "profile-A", Predicate: "claim"})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrVerifierTimeout))

		// Release the slot we held.
		<-svc.sem
	})

	t.Run("MetricsRecorded_OnSuccess", func(t *testing.T) {
		m := observability.NewInMemoryDiscoverabilityMetrics()
		want := Response{Verdict: "entailed", Confidence: 0.9, Reasoning: "yes", RawJSON: `{}`}

		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			return want, nil
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		svc.SetMetrics(m)

		_, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})
		require.NoError(t, err)
		assert.Equal(t, 1, m.VerifyVerdictCount("entailed"))
	})

	t.Run("MetricsRecorded_OnFinalError", func(t *testing.T) {
		m := observability.NewInMemoryDiscoverabilityMetrics()

		inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
			return Response{}, &ProviderError{Provider: "stub", Message: "500"}
		}}

		svc := NewRetryVerifier(inner, newTestCfg(5))
		svc.SetMetrics(m)

		_, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})
		require.Error(t, err)
		assert.Equal(t, 1, m.VerifyVerdictCount("error"))
	})
}

// TestRetryVerifier_CrossProfileIsolation verifies that a RetryVerifier scoped
// to profile A does not leak results to profile B queries. Profile isolation is
// enforced by the inner Verifier; the retry wrapper must forward ProfileID
// unchanged so the inner implementation can apply its boundary.
func TestRetryVerifierDefaultsAndNoopLogger(t *testing.T) {
	svc := NewRetryVerifier(&stubVerifier{}, newTestCfg(0), nil)
	require.Len(t, svc.sem, 0)
	require.Equal(t, 5, cap(svc.sem))
	require.NotNil(t, svc.metrics)

	svc.SetMetrics(nil)
	require.NotNil(t, svc.metrics)

	logger := noopLogProvider{}
	logger.Info("info")
	logger.Warn("warn")
	logger.Debug("debug")
	logger.Error("error", errors.New("boom"))
	require.NotNil(t, logger.With(observability.String("key", "value")))
}

func TestRetryVerifier_CrossProfileIsolation(t *testing.T) {
	const profileA = "profile-A"
	const profileB = "profile-B"

	aResponse := Response{
		Verdict:    "entailed",
		Confidence: 0.9,
		Reasoning:  "matches profile A evidence",
		RawJSON:    `{"id":"A","verdict":"entailed"}`,
	}

	// Inner stub returns data only for profileA, simulating a profile-scoped store.
	inner := &stubVerifier{fn: func(_ context.Context, req Request) (Response, error) {
		if req.ProfileID != profileA {
			return Response{}, &ProviderError{
				Provider: "stub",
				Message:  "profile not found",
			}
		}
		return aResponse, nil
	}}

	svc := NewRetryVerifier(inner, newTestCfg(5))

	// Profile A gets its own result.
	gotA, err := svc.Verify(context.Background(), Request{ProfileID: profileA, Predicate: "claim"})
	require.NoError(t, err)
	require.Equal(t, aResponse, gotA)

	// Profile B must not receive profile A's data.
	gotB, errB := svc.Verify(context.Background(), Request{ProfileID: profileB, Predicate: "claim"})
	require.Error(t, errB, "profile B must be denied")
	require.True(t, errors.Is(errB, ErrVerifierProvider))
	assert.Empty(t, gotB.RawJSON, "profile B must not receive profile A's RawJSON")
	assert.NotContains(t, gotB.RawJSON, "A", "profile A ID must not appear in profile B result")
}

func TestRetryVerifierConstructorDefaultsAndNoopLogger(t *testing.T) {
	inner := &stubVerifier{fn: func(_ context.Context, _ Request) (Response, error) {
		return Response{Verdict: "entailed"}, nil
	}}
	svc := NewRetryVerifier(inner, newTestCfg(0), nil)
	require.Equal(t, 5, cap(svc.sem))

	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	svc.SetMetrics(metrics)
	svc.SetMetrics(nil)
	_, err := svc.Verify(context.Background(), Request{ProfileID: "profile-A", Predicate: "claim"})
	require.NoError(t, err)

	var logger noopLogProvider
	logger.Info("info")
	logger.Warn("warn")
	logger.Debug("debug")
	logger.Error("error", errors.New("boom"))
	require.NotNil(t, logger.With(observability.String("k", "v")))
}
