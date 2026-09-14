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

// LogAttr is the bounded attribute shape consumed by HTTP transport. The
// transport owns this representation so logging implementations remain a
// composition concern.
type LogAttr struct {
	Key   string
	Value any
}

// LogProvider is the minimal sanitized logging surface required by HTTP.
type LogProvider interface {
	Info(string, ...LogAttr)
	Error(string, error, ...LogAttr)
	Warn(string, ...LogAttr)
	Debug(string, ...LogAttr)
	With(...LogAttr) LogProvider
}

func String(key, value string) LogAttr { return LogAttr{Key: key, Value: value} }

func Int(key string, value int) LogAttr { return LogAttr{Key: key, Value: value} }

// CredentialVerifier validates a bearer value against its stored hash.
type CredentialVerifier interface {
	Verify(context.Context, string, string) (bool, error)
}

// CredentialLookupPrefixes returns the bounded current and legacy key-prefix
// candidates for one raw bearer value. The credential policy owns its
// implementation; HTTP only consumes the injected lookup boundary.
type CredentialLookupPrefixes func(string) []string

// HTTPMetrics records one request observation for a transport-owned listener.
type HTTPMetrics interface {
	ObserveHTTPRequest(context.Context, string, string, int, time.Duration)
}
