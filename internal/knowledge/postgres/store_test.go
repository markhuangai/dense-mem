package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var (
	_ RememberPort  = (*Store)(nil)
	_ LifecyclePort = (*Store)(nil)
	_ ConflictPort  = (*Store)(nil)
)

func TestStoreExposesOnlyCapabilityPortsToComposition(t *testing.T) {
	store := NewStore(nil, nil, knowledgecontract.ConflictRuntimeConfig{})
	if store == nil {
		t.Fatal("NewStore returned nil")
	}

}

func TestRememberDiagnosticPurgerCanBeCanceledAndJoined(t *testing.T) {
	store := NewStore(nil, nil, knowledgecontract.ConflictRuntimeConfig{})
	done := store.StartRememberAttemptDiagnosticPurger(context.Background(), time.Hour, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.ShutdownRememberAttemptDiagnosticPurger(ctx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("diagnostic purger was not joined")
	}
}

func TestRememberDiagnosticPurgerJoinsDelayedPurge(t *testing.T) {
	store := NewStore(nil, nil, knowledgecontract.ConflictRuntimeConfig{})
	started := make(chan struct{})
	store.rememberDiagnosticPurgeFn = func(ctx context.Context) (int, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}
	done := store.StartRememberAttemptDiagnosticPurger(context.Background(), 25*time.Millisecond, nil)
	select {
	case <-started:
		t.Fatal("diagnostic purger ran before its first interval")
	case <-time.After(5 * time.Millisecond):
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("diagnostic purger did not begin delayed purge")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := store.ShutdownRememberAttemptDiagnosticPurger(shutdownCtx); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delayed diagnostic purger was not joined")
	}
}

func TestRememberDiagnosticPurgerTimeoutRetainsLifecycleForRetry(t *testing.T) {
	purgeStarted := make(chan struct{})
	purgeBlock := make(chan struct{})
	var purgeStartedOnce sync.Once
	store := NewStore(nil, nil, knowledgecontract.ConflictRuntimeConfig{})
	store.rememberDiagnosticPurgeFn = func(context.Context) (int, error) {
		purgeStartedOnce.Do(func() { close(purgeStarted) })
		<-purgeBlock
		return 0, nil
	}
	done := store.StartRememberAttemptDiagnosticPurger(context.Background(), 5*time.Millisecond, nil)
	select {
	case <-purgeStarted:
	case <-time.After(time.Second):
		t.Fatal("diagnostic purger did not begin purge")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := store.ShutdownRememberAttemptDiagnosticPurger(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want context deadline exceeded", err)
	}
	cancel()
	if retryDone := store.StartRememberAttemptDiagnosticPurger(context.Background(), time.Hour, nil); retryDone != done {
		t.Fatal("starting during a timed-out shutdown created a second lifecycle")
	}
	close(purgeBlock)
	joinCtx, joinCancel := context.WithTimeout(context.Background(), time.Second)
	defer joinCancel()
	if err := store.ShutdownRememberAttemptDiagnosticPurger(joinCtx); err != nil {
		t.Fatalf("retry shutdown returned error: %v", err)
	}
}
