// Package contract contains the narrow ports consumed by HTTP transport.
package contract

import (
	"context"
	"time"
)

// ConfigProvider exposes only configuration values needed by HTTP transport.
// Application and infrastructure configuration remain owned by their packages.
type ConfigProvider interface {
	GetHTTPMaxBodyBytes() int
	GetRateLimitPerMinute() int
	GetControlPortalToken() string
	GetAIVerifierModel() string
	GetAIEmbeddingModel() string
}

// BodyLimitConfig is the minimal configuration needed by the public server.
type BodyLimitConfig interface {
	GetHTTPMaxBodyBytes() int
}

// CredentialVerifier validates a bearer value against its stored hash.
type CredentialVerifier interface {
	Verify(context.Context, string, string) (bool, error)
}

// HTTPMetrics records one request observation for a transport-owned listener.
type HTTPMetrics interface {
	ObserveHTTPRequest(context.Context, string, string, int, time.Duration)
}
