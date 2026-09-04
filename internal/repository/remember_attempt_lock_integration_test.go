package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRememberAttemptLockSerializesAcrossRepositoryInstances(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	firstRepo := NewLedgerRepository(appDB, nil)
	secondRepo := NewLedgerRepository(appDB, nil)
	teamID, ownerID, key := uuid.NewString(), uuid.NewString(), "remember-lock-cross-instance"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	release := make(chan struct{})
	var secondWaited atomic.Bool
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- firstRepo.WithRememberAttemptLock(ctx, teamID, ownerID, key, func(bool) error {
			close(firstEntered)
			<-release
			return nil
		})
	}()
	<-firstEntered

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- secondRepo.WithRememberAttemptLock(ctx, teamID, ownerID, key, func(waited bool) error {
			secondWaited.Store(waited)
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("cross-instance same-key callback ran while the first lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-firstErr)
	select {
	case <-secondEntered:
	case <-ctx.Done():
		t.Fatal("cross-instance waiter did not acquire after release")
	}
	require.True(t, secondWaited.Load(), "cross-instance callback must identify that it waited")
	require.NoError(t, <-secondErr)
}

func TestRememberAttemptLockDifferentKeysUseIndependentCallbacks(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewLedgerRepository(appDB, nil)
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var group sync.WaitGroup
	errorsCh := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsCh <- repo.WithRememberAttemptLock(ctx, teamID, ownerID, uuid.NewString(), func(bool) error { return nil })
		}()
	}
	group.Wait()
	for range 2 {
		require.NoError(t, <-errorsCh)
	}
}
