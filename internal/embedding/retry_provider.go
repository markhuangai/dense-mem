package embedding

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
)

// DefaultRetryEmbeddingMaxRetries is the retry count after the first attempt.
const DefaultRetryEmbeddingMaxRetries = 3

// RetryEmbeddingProvider wraps an EmbeddingProviderInterface with retry logic.
// It implements bounded exponential backoff with jitter for transient errors.
type RetryEmbeddingProvider struct {
	inner      EmbeddingProviderInterface
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
	logger     observability.LogProvider
	apiKey     string
	metrics    observability.DiscoverabilityMetrics
}

type RetryEmbeddingOptions struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// Compile-time assertion that RetryEmbeddingProvider implements EmbeddingProviderInterface.
var _ EmbeddingProviderInterface = (*RetryEmbeddingProvider)(nil)

// NewRetryEmbeddingProvider creates a new retry wrapper around the given provider.
// The retry configuration is fixed at:
// - maxRetries: DefaultRetryEmbeddingMaxRetries
// - baseDelay: 200ms
// - maxDelay: 5s
func NewRetryEmbeddingProvider(inner EmbeddingProviderInterface, logger observability.LogProvider) *RetryEmbeddingProvider {
	return &RetryEmbeddingProvider{
		inner:      inner,
		maxRetries: DefaultRetryEmbeddingMaxRetries,
		baseDelay:  200 * time.Millisecond,
		maxDelay:   5 * time.Second,
		logger:     logger,
		metrics:    observability.NoopDiscoverabilityMetrics(),
	}
}

// NewRetryEmbeddingProviderWithKey creates a new retry wrapper with the API key
// for sanitization purposes.
func NewRetryEmbeddingProviderWithKey(inner EmbeddingProviderInterface, logger observability.LogProvider, apiKey string) *RetryEmbeddingProvider {
	return NewRetryEmbeddingProviderWithKeyAndOptions(inner, logger, apiKey, RetryEmbeddingOptions{})
}

func NewRetryEmbeddingProviderWithKeyAndOptions(inner EmbeddingProviderInterface, logger observability.LogProvider, apiKey string, opts RetryEmbeddingOptions) *RetryEmbeddingProvider {
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = DefaultRetryEmbeddingMaxRetries
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = 200 * time.Millisecond
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = 5 * time.Second
	}
	return &RetryEmbeddingProvider{
		inner:      inner,
		maxRetries: opts.MaxRetries,
		baseDelay:  opts.BaseDelay,
		maxDelay:   opts.MaxDelay,
		logger:     logger,
		apiKey:     apiKey,
		metrics:    observability.NoopDiscoverabilityMetrics(),
	}
}

// SetMetrics attaches a DiscoverabilityMetrics recorder. A nil value is
// normalised to the noop recorder so call sites need no nil checks.
// Intended for bootstrap-time wiring; not safe to call mid-request.
func (p *RetryEmbeddingProvider) SetMetrics(m observability.DiscoverabilityMetrics) {
	if m == nil {
		m = observability.NoopDiscoverabilityMetrics()
	}
	p.metrics = m
}

// Embed returns the embedding for a single text with retry logic.
func (p *RetryEmbeddingProvider) Embed(ctx context.Context, text string) ([]float32, string, error) {
	var lastErr error
	configuredModel := p.inner.ModelName()

	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		attemptStart := time.Now()
		vec, model, err := p.inner.Embed(ctx, text)
		dur := float64(time.Since(attemptStart).Milliseconds())

		if err == nil {
			observability.RecordEmbeddingLatency(ctx, p.metrics, model, dur, "ok")
			return vec, model, nil
		}

		code := classifyEmbeddingError(err)
		observability.RecordEmbeddingLatency(ctx, p.metrics, configuredModel, dur, code)

		lastErr = err

		// Check if we should retry
		if !p.shouldRetry(err) {
			break
		}

		// Check if context was cancelled or deadline exceeded
		if ctx.Err() != nil {
			break
		}

		// Don't sleep after the last attempt
		if attempt < p.maxRetries {
			delay := p.retryDelay(attempt, err)
			select {
			case <-ctx.Done():
				break
			case <-time.After(delay):
				continue
			}
		}
	}

	// Final failure — increment error counter by classified code.
	observability.RecordEmbeddingError(ctx, p.metrics, configuredModel, classifyEmbeddingError(lastErr))

	// Sanitize the error before returning
	return nil, "", SanitizeError(lastErr, p.apiKey)
}

// EmbedBatch returns embeddings for multiple texts with retry logic.
func (p *RetryEmbeddingProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	var lastErr error
	configuredModel := p.inner.ModelName()

	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		attemptStart := time.Now()
		vecs, model, err := p.inner.EmbedBatch(ctx, texts)
		dur := float64(time.Since(attemptStart).Milliseconds())

		if err == nil {
			observability.RecordEmbeddingLatency(ctx, p.metrics, model, dur, "ok")
			return vecs, model, nil
		}

		code := classifyEmbeddingError(err)
		observability.RecordEmbeddingLatency(ctx, p.metrics, configuredModel, dur, code)

		lastErr = err

		// Check if we should retry
		if !p.shouldRetry(err) {
			break
		}

		// Check if context was cancelled or deadline exceeded
		if ctx.Err() != nil {
			break
		}

		// Don't sleep after the last attempt
		if attempt < p.maxRetries {
			delay := p.retryDelay(attempt, err)
			select {
			case <-ctx.Done():
				break
			case <-time.After(delay):
				continue
			}
		}
	}

	observability.RecordEmbeddingError(ctx, p.metrics, configuredModel, classifyEmbeddingError(lastErr))

	// Sanitize the error before returning
	return nil, "", SanitizeError(lastErr, p.apiKey)
}

// classifyEmbeddingError maps a provider error to a coarse-grained tag used
// by metrics. Unknown errors fall back to "error" so the recorder always
// sees a bounded label set — important for Prometheus cardinality.
func classifyEmbeddingError(err error) string {
	if err == nil {
		return "ok"
	}
	metadata := ClassifyFailure(err)
	switch metadata.Code {
	case "provider_timeout":
		return "timeout"
	case "provider_rate_limited":
		return "rate_limited"
	case "provider_network_error":
		return "network_error"
	case "provider_server_error":
		return "error"
	case "provider_quota_exhausted", "provider_authentication_failed", "provider_permission_denied", "provider_contract_rejected", "provider_response_invalid":
		return metadata.Code
	default:
		return "error"
	}
}

// ModelName returns the configured model identifier.
func (p *RetryEmbeddingProvider) ModelName() string {
	return p.inner.ModelName()
}

// Dimensions returns the configured vector length.
func (p *RetryEmbeddingProvider) Dimensions() int {
	return p.inner.Dimensions()
}

// IsAvailable returns true when the provider is configured to serve requests.
func (p *RetryEmbeddingProvider) IsAvailable() bool {
	return p.inner.IsAvailable()
}

// shouldRetry determines if an error should trigger a retry.
// Retries are allowed for:
// - HTTP 429 (rate limit)
// - HTTP 5xx (server errors)
// - Network timeout errors
// - Context deadline exceeded
// No retries for 4xx except 429.
func (p *RetryEmbeddingProvider) shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return ClassifyFailure(err).Class == "transient"
}

func (p *RetryEmbeddingProvider) retryDelay(attempt int, err error) time.Duration {
	var httpErr *ProviderHTTPError
	if errors.As(err, &httpErr) {
		if httpErr.RetryAfter > 0 {
			return httpErr.RetryAfter
		}
		if httpErr.Status == 429 {
			return 10 * time.Second
		}
	}
	var rateLimitErr *RateLimitError
	if errors.As(err, &rateLimitErr) && rateLimitErr.RetryAfter > 0 {
		return time.Duration(rateLimitErr.RetryAfter) * time.Second
	}
	return p.calculateDelay(attempt)
}

// calculateDelay computes the backoff delay for the given attempt.
// Uses exponential backoff with jitter: delay = min(baseDelay * 2^attempt, maxDelay) + jitter
func (p *RetryEmbeddingProvider) calculateDelay(attempt int) time.Duration {
	// Exponential backoff: baseDelay * 2^attempt
	delay := p.baseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay > p.maxDelay {
			delay = p.maxDelay
			break
		}
	}

	// Add jitter: 0-100ms
	jitter := time.Duration(rand.Intn(100)) * time.Millisecond
	delay += jitter

	// Cap at maxDelay
	if delay > p.maxDelay {
		delay = p.maxDelay
	}

	return delay
}
