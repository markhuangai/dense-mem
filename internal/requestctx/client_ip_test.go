package requestctx

import (
	"context"
	"testing"
)

func TestClientIPFromContext_ReturnsIPSetByWithClientIP(t *testing.T) {
	ctx := WithClientIP(context.Background(), "192.168.1.101")
	if got := ClientIPFromContext(ctx); got != "192.168.1.101" {
		t.Errorf("ClientIPFromContext() = %q; want 192.168.1.101", got)
	}
}

func TestClientIPFromContext_EmptyWhenUnset(t *testing.T) {
	if got := ClientIPFromContext(context.Background()); got != "" {
		t.Errorf("ClientIPFromContext() = %q; want empty string", got)
	}
}

func TestClientIPFromContext_NilSafe(t *testing.T) {
	//lint:ignore SA1012 ClientIPFromContext intentionally accepts nil for defensive request logging paths.
	if got := ClientIPFromContext(nil); got != "" {
		t.Errorf("ClientIPFromContext(nil) = %q; want empty string", got)
	}
}

func TestWithClientIP_TrimsWhitespace(t *testing.T) {
	ctx := WithClientIP(context.Background(), "  192.168.1.101  ")
	if got := ClientIPFromContext(ctx); got != "192.168.1.101" {
		t.Errorf("ClientIPFromContext() = %q; want 192.168.1.101", got)
	}
}

func TestClientIPFromContext_DoesNotLeakAcrossKeys(t *testing.T) {
	type other struct{}
	ctx := context.WithValue(context.Background(), other{}, "192.168.1.101")
	if got := ClientIPFromContext(ctx); got != "" {
		t.Errorf("ClientIPFromContext should ignore unrelated keys, got %q", got)
	}
}
