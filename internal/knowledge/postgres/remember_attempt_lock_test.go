package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestRememberAttemptLockLocalWaiterReturnsItsOwnCallbackResult(t *testing.T) {
	teamID, ownerProfileID, idempotencyKey := uuid.NewString(), uuid.NewString(), "local-waiter-result"
	localKey := rememberIdempotencyLockNamespace + teamID + ":" + ownerProfileID + ":" + idempotencyKey
	ready := make(chan struct{})
	close(ready)
	store := &Store{
		db: &gorm.DB{},
		rememberIdempotencyLocks: map[string]*rememberIdempotencyLockEntry{
			localKey: {ready: ready, err: context.Canceled, callbackStarted: true},
		},
	}
	var waited bool

	err := store.WithRememberAttemptLock(context.Background(), teamID, ownerProfileID, idempotencyKey, func(callbackWaited bool) error {
		waited = callbackWaited
		return nil
	})

	if err != nil {
		t.Fatalf("local waiter returned owner error: %v", err)
	}
	if !waited {
		t.Fatal("local waiter callback did not receive waited=true")
	}
}

func TestRememberAttemptLockLocalWaiterPreservesPreCallbackFailure(t *testing.T) {
	teamID, ownerProfileID, idempotencyKey := uuid.NewString(), uuid.NewString(), "local-waiter-failure"
	localKey := rememberIdempotencyLockNamespace + teamID + ":" + ownerProfileID + ":" + idempotencyKey
	ready := make(chan struct{})
	close(ready)
	store := &Store{
		db: &gorm.DB{},
		rememberIdempotencyLocks: map[string]*rememberIdempotencyLockEntry{
			localKey: {ready: ready, err: ErrRememberIdempotencyBusy},
		},
	}
	called := false

	err := store.WithRememberAttemptLock(context.Background(), teamID, ownerProfileID, idempotencyKey, func(bool) error {
		called = true
		return nil
	})

	if !errors.Is(err, ErrRememberIdempotencyBusy) {
		t.Fatalf("local waiter error = %v, want idempotency busy", err)
	}
	if called {
		t.Fatal("local waiter callback ran after a pre-callback owner failure")
	}
}
