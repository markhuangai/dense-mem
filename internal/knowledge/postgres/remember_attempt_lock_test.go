package postgres

import (
	"context"
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
			localKey: {ready: ready, err: context.Canceled},
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
