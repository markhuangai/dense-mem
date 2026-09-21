package requestctx

import (
	"context"
	"testing"
)

func TestRememberInvocationIDSinkPublishesOnlyThroughInstalledHolder(t *testing.T) {
	ctx := WithRememberInvocationIDSink(context.Background())
	SetRememberInvocationID(ctx, " invocation-1 ")
	if got := RememberInvocationIDFromContext(ctx); got != "invocation-1" {
		t.Fatalf("RememberInvocationIDFromContext = %q; want invocation-1", got)
	}
	SetRememberInvocationID(context.Background(), "ignored")
	if got := RememberInvocationIDFromContext(context.Background()); got != "" {
		t.Fatalf("RememberInvocationIDFromContext(unset) = %q; want empty", got)
	}
	var nilContext context.Context
	SetRememberInvocationID(nilContext, "ignored")
	if got := RememberInvocationIDFromContext(nilContext); got != "" {
		t.Fatalf("RememberInvocationIDFromContext(nil) = %q; want empty", got)
	}
}
