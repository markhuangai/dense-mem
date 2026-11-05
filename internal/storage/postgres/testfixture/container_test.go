package testfixture

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestStartupPolicy(t *testing.T) {
	rootless := errors.New("start container: failed to set up container networking: error while calling RootlessKit PortManager.AddPort(): listen tcp4 0.0.0.0:33014: bind: address already in use")
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"rootless collision", rootless, true},
		{"Docker allocation", errors.New("driver failed programming external connectivity on endpoint fixture: Bind for 0.0.0.0:33014 failed: port is already allocated"), true},
		{"host port bind", errors.New("failed to bind host port for 0.0.0.0:33014: address already in use"), true},
		{"plain initialization error", errors.New("startup-init-failure: address already in use"), false},
		{"initialization exit", errors.New("container exited with code 17"), false},
		{"network permission", errors.New("failed programming external connectivity: operation not permitted"), false},
		{"registry denied", errors.New("pull image: unauthorized"), false},
		{"readiness deadline", context.DeadlineExceeded, false},
		{"cancelled collision", errors.Join(rootless, context.Canceled), false},
		{"expired collision", errors.Join(rootless, context.DeadlineExceeded), false},
		{"no error", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := publishedPortCollision(tc.err); got != tc.retry {
				t.Fatalf("publishedPortCollision(%v) = %t, want %t", tc.err, got, tc.retry)
			}
		})
	}
	if !publishedPortCollision(fmt.Errorf("run postgres: %w", rootless)) {
		t.Fatal("wrapped Docker startup collision must remain retryable")
	}
}

func TestCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	container, err := Run(ctx, "invalid-image-must-not-be-acquired")
	if container != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run with cancelled context = (%v, %v), want no container and cancellation", container, err)
	}
}

func TestInvalidLogLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "invalid")
	container, err := Run(context.Background(), "invalid-image-must-not-be-acquired")
	if container != nil || err == nil {
		t.Fatalf("Run with invalid log level = (%v, %v), want configuration failure", container, err)
	}
}
