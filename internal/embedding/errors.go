package embedding

import (
	"time"

	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
)

var ErrEmbeddingTimeout = embeddingcontract.ErrEmbeddingTimeout
var ErrEmbeddingRateLimit = embeddingcontract.ErrEmbeddingRateLimit
var ErrEmbeddingProvider = embeddingcontract.ErrEmbeddingProvider

type TimeoutError = embeddingcontract.TimeoutError
type RateLimitError = embeddingcontract.RateLimitError
type ProviderError = embeddingcontract.ProviderError
type ProviderHTTPError = embeddingcontract.ProviderHTTPError
type FailureMetadata = embeddingcontract.FailureMetadata

const MaxProviderRetryAfter = embeddingcontract.MaxProviderRetryAfter

func ClassifyFailure(err error) FailureMetadata { return embeddingcontract.ClassifyFailure(err) }

func boundedRetryAfter(value time.Duration) time.Duration {
	return embeddingcontract.BoundedRetryAfter(value)
}
