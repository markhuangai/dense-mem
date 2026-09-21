package requestctx

import (
	"context"
	"strings"
	"sync"
)

type rememberInvocationIDContextKey struct{}

type rememberInvocationIDHolder struct {
	mu    sync.RWMutex
	value string
}

// WithRememberInvocationIDSink installs a private holder that application code
// can populate for completion-time transport diagnostics.
func WithRememberInvocationIDSink(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, rememberInvocationIDContextKey{}, &rememberInvocationIDHolder{})
}

// SetRememberInvocationID publishes a server-generated invocation identifier
// to the request's completion-time transport logger.
func SetRememberInvocationID(ctx context.Context, invocationID string) {
	if ctx == nil {
		return
	}
	holder, _ := ctx.Value(rememberInvocationIDContextKey{}).(*rememberInvocationIDHolder)
	if holder == nil {
		return
	}
	holder.mu.Lock()
	holder.value = strings.TrimSpace(invocationID)
	holder.mu.Unlock()
}

func RememberInvocationIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	holder, _ := ctx.Value(rememberInvocationIDContextKey{}).(*rememberInvocationIDHolder)
	if holder == nil {
		return ""
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return holder.value
}
