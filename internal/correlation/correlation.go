// Package correlation carries request correlation IDs through the context.
//
// This package is the neutral carrier between the HTTP middleware that seeds
// the ID and downstream services (audit, logs, metrics) that read it. Placing
// the key here keeps the service layer from importing http/middleware.
package correlation

import (
	"context"

	"github.com/google/uuid"
)

type contextKey struct{}

type value struct {
	id             string
	clientProvided bool
}

// WithID returns a new context carrying id as the correlation identifier.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, value{id: id})
}

// WithClientProvidedID carries a correlation identifier supplied by an HTTP
// client. Operator attribution retains it only when authentication binds the
// request and the value passes IsSafeClientID.
func WithClientProvidedID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, value{id: id, clientProvided: true})
}

// FromContext returns the correlation ID previously set with WithID.
// Returns an empty string when no id is present.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	switch stored := ctx.Value(contextKey{}).(type) {
	case value:
		return stored.id
	case string:
		return stored
	}
	return ""
}

// IsClientProvided reports whether the current correlation identifier came
// from an HTTP client rather than an internal caller.
func IsClientProvided(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	stored, ok := ctx.Value(contextKey{}).(value)
	return ok && stored.clientProvided
}

// IsSafeClientID reports whether a client-provided correlation ID has the
// server-safe UUID format allowed for trusted operator attribution.
func IsSafeClientID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}
