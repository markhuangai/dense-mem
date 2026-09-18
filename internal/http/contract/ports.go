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

// ContextLogProvider is the optional context-aware extension used when the
// transport has an authenticated request context to preserve.
type ContextLogProvider interface {
	LogProvider
	InfoContext(context.Context, string, ...LogAttr)
	ErrorContext(context.Context, string, error, ...LogAttr)
	WarnContext(context.Context, string, ...LogAttr)
	DebugContext(context.Context, string, ...LogAttr)
}

func LogInfoContext(ctx context.Context, logger LogProvider, message string, attrs ...LogAttr) {
	if logger == nil {
		return
	}
	if contextual, ok := logger.(ContextLogProvider); ok {
		contextual.InfoContext(ctx, message, attrs...)
		return
	}
	logger.Info(message, attrs...)
}

func LogErrorContext(ctx context.Context, logger LogProvider, message string, err error, attrs ...LogAttr) {
	if logger == nil {
		return
	}
	if contextual, ok := logger.(ContextLogProvider); ok {
		contextual.ErrorContext(ctx, message, err, attrs...)
		return
	}
	logger.Error(message, err, attrs...)
}

func LogWarnContext(ctx context.Context, logger LogProvider, message string, attrs ...LogAttr) {
	if logger == nil {
		return
	}
	if contextual, ok := logger.(ContextLogProvider); ok {
		contextual.WarnContext(ctx, message, attrs...)
		return
	}
	logger.Warn(message, attrs...)
}

func LogDebugContext(ctx context.Context, logger LogProvider, message string, attrs ...LogAttr) {
	if logger == nil {
		return
	}
	if contextual, ok := logger.(ContextLogProvider); ok {
		contextual.DebugContext(ctx, message, attrs...)
		return
	}
	logger.Debug(message, attrs...)
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
