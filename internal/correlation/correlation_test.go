package correlation

import (
	"context"
	"testing"
)

func TestFromContext_ReturnsIDSetByWithID(t *testing.T) {
	ctx := WithID(context.Background(), "abc-123")
	if got := FromContext(ctx); got != "abc-123" {
		t.Errorf("FromContext() = %q; want %q", got, "abc-123")
	}
	if IsClientProvided(ctx) {
		t.Fatal("WithID must not mark an internal correlation identifier as client-provided")
	}
}

func TestWithClientProvidedIDMarksSource(t *testing.T) {
	ctx := WithClientProvidedID(context.Background(), "header-correlation")
	if got := FromContext(ctx); got != "header-correlation" {
		t.Fatalf("FromContext() = %q; want %q", got, "header-correlation")
	}
	if !IsClientProvided(ctx) {
		t.Fatal("WithClientProvidedID must mark the correlation identifier as client-provided")
	}
}

func TestIsSafeClientIDRequiresUUIDFormat(t *testing.T) {
	if !IsSafeClientID("123e4567-e89b-12d3-a456-426614174000") {
		t.Fatal("canonical UUID should be safe for operator attribution")
	}
	if IsSafeClientID("dm_live_client_secret") {
		t.Fatal("credential-shaped correlation ID must not be trusted")
	}
}

func TestFromContext_EmptyWhenUnset(t *testing.T) {
	if got := FromContext(context.Background()); got != "" {
		t.Errorf("FromContext() = %q; want empty string", got)
	}
}

func TestFromContext_NilSafe(t *testing.T) {
	// Defensive: services sometimes receive nil ctx from tests; must not panic.
	if got := FromContext(nilContext()); got != "" {
		t.Errorf("FromContext(nil) = %q; want empty string", got)
	}
}

func nilContext() context.Context {
	return nil
}

func TestWithID_DoesNotLeakAcrossKeys(t *testing.T) {
	// Using a different context key type must not match the correlation key.
	type other struct{}
	ctx := context.WithValue(context.Background(), other{}, "impostor")
	if got := FromContext(ctx); got != "" {
		t.Errorf("FromContext should ignore unrelated keys, got %q", got)
	}
}
