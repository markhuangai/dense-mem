package embedding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testNopLogger is a no-op logger for tests
type testNopLogger struct{}

func (l *testNopLogger) Info(msg string, attrs ...observability.LogAttr)               {}
func (l *testNopLogger) Error(msg string, err error, attrs ...observability.LogAttr)   {}
func (l *testNopLogger) Warn(msg string, attrs ...observability.LogAttr)               {}
func (l *testNopLogger) Debug(msg string, attrs ...observability.LogAttr)              {}
func (l *testNopLogger) With(attrs ...observability.LogAttr) observability.LogProvider { return l }

func newTestLogger() observability.LogProvider { return &testNopLogger{} }

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "temporary timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return false }

func TestRetryProvider_Retries5xxUpToMax(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			return nil, "", &ProviderHTTPError{Status: 500}
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	_, _, err := p.Embed(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 4, calls, "1 initial + 3 retries")
	assert.NotContains(t, err.Error(), "bad")
	assert.NotContains(t, err.Error(), "sk-")
}

func TestRetryProvider_NoRetryOn400(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			return nil, "", &ProviderHTTPError{Status: 400}
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	_, _, err := p.Embed(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}

func TestRetryProvider_Retries429(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			if calls < 3 {
				return nil, "", &ProviderHTTPError{Status: 429, Message: "rate limited"}
			}
			return []float32{0.1, 0.2}, "model", nil
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	vec, model, err := p.Embed(context.Background(), "x")
	require.NoError(t, err)
	assert.Equal(t, 3, calls, "failed twice, succeeded on third")
	assert.Equal(t, []float32{0.1, 0.2}, vec)
	assert.Equal(t, "model", model)
}

func TestRetryProviderDoesNotRetryInsufficientQuota429(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(context.Context, string) ([]float32, string, error) {
			calls++
			return nil, "", &ProviderHTTPError{Status: 429, Code: "insufficient_quota", Type: "insufficient_quota"}
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	_, _, err := p.Embed(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	metadata := ClassifyFailure(err)
	assert.Equal(t, "provider_action_required", metadata.Class)
	assert.Equal(t, "provider_quota_exhausted", metadata.Code)
}

func TestRetryProvider_SetMetrics(t *testing.T) {
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string) ([]float32, string, error) {
			return []float32{0.1}, "model", nil
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	p.SetMetrics(metrics)

	_, _, err := p.Embed(context.Background(), "x")
	require.NoError(t, err)

	samples := metrics.EmbeddingSamples()
	require.Len(t, samples, 1)
	assert.Equal(t, "ok", samples[0].Outcome)

	p.SetMetrics(nil)
	_, _, err = p.Embed(context.Background(), "x")
	require.NoError(t, err)
}

func TestRetryProvider_ClassifyEmbeddingError(t *testing.T) {
	assert.Equal(t, "ok", classifyEmbeddingError(nil))
	assert.Equal(t, "timeout", classifyEmbeddingError(context.DeadlineExceeded))
	assert.Equal(t, "timeout", classifyEmbeddingError(&TimeoutError{Provider: "stub", Message: "deadline"}))
	assert.Equal(t, "timeout", classifyEmbeddingError(timeoutNetError{}))
	assert.Equal(t, "rate_limited", classifyEmbeddingError(&RateLimitError{Provider: "stub", Message: "429"}))
	assert.Equal(t, "rate_limited", classifyEmbeddingError(&ProviderHTTPError{Status: 429}))
	assert.Equal(t, "error", classifyEmbeddingError(&ProviderHTTPError{Status: 500}))
	assert.Equal(t, "error", classifyEmbeddingError(errors.New("boom")))
}

func TestRetryProvider_ShouldRetryDirectErrorClasses(t *testing.T) {
	p := NewRetryEmbeddingProvider(&MockEmbeddingProvider{}, newTestLogger())

	assert.False(t, p.shouldRetry(nil))
	assert.True(t, p.shouldRetry(timeoutNetError{}))
	assert.True(t, p.shouldRetry(&TimeoutError{Provider: "stub", Message: "deadline"}))
	assert.True(t, p.shouldRetry(&RateLimitError{Provider: "stub", Message: "429"}))
	assert.False(t, p.shouldRetry(errors.New("boom")))
}

func TestRetryProvider_ContextCancelStopsRetry(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			return nil, "", &ProviderHTTPError{Status: 500}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	p.SetMetrics(metrics)
	_, _, err := p.Embed(ctx, "x")
	require.ErrorIs(t, err, context.Canceled)
	var providerErr *ProviderHTTPError
	assert.NotErrorAs(t, err, &providerErr)
	assert.Equal(t, 1, calls, "should stop after context cancellation")
	assert.Zero(t, metrics.EmbeddingErrorCount("network_error"))
}

func TestRetryProviderCancellationDuringDelayDoesNotStartAnotherCall(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			firstCall := make(chan struct{})
			var calls int
			inner := &MockEmbeddingProvider{
				EmbedFunc: func(context.Context, string) ([]float32, string, error) {
					calls++
					if calls == 1 {
						close(firstCall)
					}
					return nil, "", &ProviderHTTPError{Status: 500}
				},
				EmbedBatchFunc: func(context.Context, []string) ([][]float32, string, error) {
					calls++
					if calls == 1 {
						close(firstCall)
					}
					return nil, "", &ProviderHTTPError{Status: 500}
				},
			}
			provider := NewRetryEmbeddingProviderWithKeyAndOptions(inner, newTestLogger(), "", RetryEmbeddingOptions{BaseDelay: time.Second, MaxDelay: time.Second})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if batch {
					_, _, err := provider.EmbedBatch(ctx, []string{"x"})
					done <- err
					return
				}
				_, _, err := provider.Embed(ctx, "x")
				done <- err
			}()
			<-firstCall
			time.Sleep(20 * time.Millisecond)
			cancel()
			err := <-done
			require.ErrorIs(t, err, context.Canceled)
			var providerErr *ProviderHTTPError
			assert.NotErrorAs(t, err, &providerErr)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestRetryProviderDeadlineDuringDelayPreservesProviderFailure(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			inner := &MockEmbeddingProvider{
				EmbedFunc: func(context.Context, string) ([]float32, string, error) {
					return nil, "", &ProviderHTTPError{Status: 429}
				},
				EmbedBatchFunc: func(context.Context, []string) ([][]float32, string, error) {
					return nil, "", &ProviderHTTPError{Status: 429}
				},
			}
			provider := NewRetryEmbeddingProvider(inner, newTestLogger())
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			var err error
			if batch {
				_, _, err = provider.EmbedBatch(ctx, []string{"x"})
			} else {
				_, _, err = provider.Embed(ctx, "x")
			}

			var providerErr *ProviderHTTPError
			require.ErrorAs(t, err, &providerErr)
			assert.Equal(t, 429, providerErr.Status)
			assert.NotErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestRetryProviderCapsProviderRetryHintsAtPolicyMaximum(t *testing.T) {
	provider := NewRetryEmbeddingProvider(&MockEmbeddingProvider{}, newTestLogger())
	assert.Equal(t, MaxProviderRetryAfter, provider.retryDelay(0, &ProviderHTTPError{Status: 429, RetryAfter: time.Hour}))
	assert.Equal(t, MaxProviderRetryAfter, provider.retryDelay(0, &RateLimitError{RetryAfter: 3600}))
}

func TestRetryProvider_SuccessAfterOneRetry(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			if calls == 1 {
				return nil, "", &ProviderHTTPError{Status: 500}
			}
			return []float32{0.5}, "model", nil
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	vec, model, err := p.Embed(context.Background(), "x")
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []float32{0.5}, vec)
	assert.Equal(t, "model", model)
}

func TestRetryProvider_FailAfterMaxRetries(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			return nil, "", &ProviderHTTPError{Status: 503, Message: "service unavailable"}
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	_, _, err := p.Embed(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 4, calls, "1 initial + 3 retries")
}

func TestRetryProvider_ApiKeyScrubbed(t *testing.T) {
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			return nil, "", errors.New("failed with key sk-secret123 and Bearer sk-secret123")
		},
	}
	p := NewRetryEmbeddingProviderWithKey(inner, newTestLogger(), "sk-secret123")
	_, _, err := p.Embed(context.Background(), "x")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sk-secret123")
	assert.Contains(t, err.Error(), "[REDACTED]")
}

func TestRetryProvider_NoRetryOn401(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			return nil, "", &ProviderHTTPError{Status: 401, Message: "unauthorized"}
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	_, _, err := p.Embed(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 1, calls, "401 should not retry")
}

func TestRetryProvider_NoRetryOn404(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			return nil, "", &ProviderHTTPError{Status: 404, Message: "not found"}
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	_, _, err := p.Embed(context.Background(), "x")
	require.Error(t, err)
	assert.Equal(t, 1, calls, "404 should not retry")
}

func TestRetryProvider_RetriesOnContextDeadlineExceeded(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, _ string) ([]float32, string, error) {
			calls++
			if calls < 2 {
				return nil, "", context.DeadlineExceeded
			}
			return []float32{0.3}, "model", nil
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	vec, _, err := p.Embed(context.Background(), "x")
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []float32{0.3}, vec)
}

func TestRetryProvider_PassesThroughSuccess(t *testing.T) {
	inner := &MockEmbeddingProvider{
		EmbedFunc: func(ctx context.Context, text string) ([]float32, string, error) {
			return []float32{0.1, 0.2}, "test-model", nil
		},
		ModelNameResult: "test-model",
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	vec, model, err := p.Embed(context.Background(), "test")
	require.NoError(t, err)
	assert.Equal(t, []float32{0.1, 0.2}, vec)
	assert.Equal(t, "test-model", model)
}

func TestRetryProvider_EdimBatchRetries(t *testing.T) {
	var calls int
	inner := &MockEmbeddingProvider{
		EmbedBatchFunc: func(ctx context.Context, texts []string) ([][]float32, string, error) {
			calls++
			if calls < 2 {
				return nil, "", &ProviderHTTPError{Status: 500}
			}
			result := make([][]float32, len(texts))
			for i := range result {
				result[i] = []float32{0.1}
			}
			return result, "model", nil
		},
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	vecs, model, err := p.EmbedBatch(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Len(t, vecs, 2)
	assert.Equal(t, "model", model)
}

func TestRetryProvider_DimensionsAndModel(t *testing.T) {
	inner := &MockEmbeddingProvider{
		DimensionsResult: 1536,
		ModelNameResult:  "text-embedding-ada-002",
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	assert.Equal(t, 1536, p.Dimensions())
	assert.Equal(t, "text-embedding-ada-002", p.ModelName())
}

func TestRetryProvider_IsAvailable(t *testing.T) {
	inner := &MockEmbeddingProvider{
		IsAvailableResult: true,
	}
	p := NewRetryEmbeddingProvider(inner, newTestLogger())
	assert.True(t, p.IsAvailable())
}

func TestRetryProvider_WithOptions(t *testing.T) {
	inner := &MockEmbeddingProvider{}
	p := NewRetryEmbeddingProviderWithKeyAndOptions(inner, newTestLogger(), "key", RetryEmbeddingOptions{
		MaxRetries: 12,
		BaseDelay:  3 * time.Second,
		MaxDelay:   45 * time.Second,
	})

	assert.Equal(t, 12, p.maxRetries)
	assert.Equal(t, 3*time.Second, p.baseDelay)
	assert.Equal(t, 45*time.Second, p.maxDelay)
}

func TestRetryProvider_BackoffDelays(t *testing.T) {
	p := &RetryEmbeddingProvider{
		maxRetries: 3,
		baseDelay:  200 * time.Millisecond,
		maxDelay:   5 * time.Second,
	}

	// Test that delays increase with exponential backoff
	delay0 := p.calculateDelay(0)
	delay1 := p.calculateDelay(1)
	delay2 := p.calculateDelay(2)
	delay3 := p.calculateDelay(3)

	// Base delay is 200ms, so delays should roughly be:
	// attempt 0: ~200ms (with jitter)
	// attempt 1: ~400ms (with jitter)
	// attempt 2: ~800ms (with jitter)
	// attempt 3: ~1600ms (with jitter)

	assert.GreaterOrEqual(t, delay0, 200*time.Millisecond)
	assert.LessOrEqual(t, delay0, 300*time.Millisecond) // 200 + 100 jitter

	assert.GreaterOrEqual(t, delay1, 400*time.Millisecond)
	assert.LessOrEqual(t, delay1, 500*time.Millisecond) // 400 + 100 jitter

	assert.GreaterOrEqual(t, delay2, 800*time.Millisecond)
	assert.LessOrEqual(t, delay2, 900*time.Millisecond) // 800 + 100 jitter

	assert.GreaterOrEqual(t, delay3, 1600*time.Millisecond)
	assert.LessOrEqual(t, delay3, 1700*time.Millisecond) // 1600 + 100 jitter
}

func TestRetryProvider_BackoffCapsAtMaxDelay(t *testing.T) {
	p := &RetryEmbeddingProvider{
		maxRetries: 10,
		baseDelay:  200 * time.Millisecond,
		maxDelay:   500 * time.Millisecond,
	}

	// With maxDelay of 500ms, even high attempt numbers should cap
	for i := 0; i < 20; i++ {
		delay := p.calculateDelay(i)
		assert.LessOrEqual(t, delay, 600*time.Millisecond, "delay should cap at maxDelay + jitter")
	}
}
