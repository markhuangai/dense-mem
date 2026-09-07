package repository

import (
	"context"
	"database/sql"
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

func TestRememberAttemptLockReleasesAfterContextCancellation(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewLedgerRepository(appDB, nil)
	teamID, ownerID, key := uuid.NewString(), uuid.NewString(), "remember-lock-cancel"
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- repo.WithRememberAttemptLock(ctx, teamID, ownerID, key, func(bool) error {
			close(entered)
			cancel()
			return context.Canceled
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("remember lock callback did not start")
	}
	require.ErrorIs(t, <-errCh, context.Canceled)

	followupCtx, followupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer followupCancel()
	require.NoError(t, repo.WithRememberAttemptLock(followupCtx, teamID, ownerID, key, func(bool) error { return nil }))
}

func TestRememberAttemptLockReleasesAfterCallbackPanic(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewLedgerRepository(appDB, nil)
	teamID, ownerID, key := uuid.NewString(), uuid.NewString(), "remember-lock-panic"
	func() {
		defer func() {
			require.Equal(t, "remember callback panic", recover())
		}()
		_ = repo.WithRememberAttemptLock(context.Background(), teamID, ownerID, key, func(bool) error {
			panic("remember callback panic")
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, repo.WithRememberAttemptLock(ctx, teamID, ownerID, key, func(bool) error { return nil }))
}

func TestRememberAttemptLockBoundsDifferentKeysByPoolAdmission(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(sharedAdvisoryLockAdmissionLimit + 1)
	sqlDB.SetMaxIdleConns(sharedAdvisoryLockAdmissionLimit + 1)
	repo := NewLedgerRepository(appDB, nil)
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	entered := make(chan struct{}, sharedAdvisoryLockAdmissionLimit)
	errsCh := make(chan error, sharedAdvisoryLockAdmissionLimit)
	for index := 0; index < sharedAdvisoryLockAdmissionLimit; index++ {
		key := uuid.NewString()
		go func() {
			errsCh <- repo.WithRememberAttemptLock(ctx, teamID, ownerID, key, func(bool) error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for index := 0; index < sharedAdvisoryLockAdmissionLimit; index++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("remember lock callback did not reach the admission bound")
		}
	}

	extraEntered := make(chan struct{})
	extraErr := make(chan error, 1)
	go func() {
		extraErr <- repo.WithRememberAttemptLock(ctx, teamID, ownerID, uuid.NewString(), func(bool) error {
			close(extraEntered)
			return nil
		})
	}()
	select {
	case <-extraEntered:
		t.Fatal("remember lock callback exceeded the admission bound")
	case err := <-extraErr:
		require.ErrorIs(t, err, ErrRememberIdempotencyBusy)
	case <-time.After(5 * time.Second):
		t.Fatal("remember lock admission did not return while the bound was full")
	}
	close(release)
	for index := 0; index < sharedAdvisoryLockAdmissionLimit; index++ {
		require.NoError(t, <-errsCh)
	}
}

func TestRememberAttemptLockRejectsPoolWithoutApplicationCapacity(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	repo := NewLedgerRepository(appDB, nil)
	called := false
	err = repo.WithRememberAttemptLock(context.Background(), uuid.NewString(), uuid.NewString(), "remember-lock-no-capacity", func(bool) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, ErrRememberIdempotencyBusy)
	require.False(t, called)
}

func TestRememberAttemptLockDiscardsFailedCleanupConnection(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lockConn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)

	require.NoError(t, discardAdvisoryLockConnection(lockConn))
	require.ErrorIs(t, lockConn.PingContext(ctx), sql.ErrConnDone)
	require.NoError(t, sqlDB.PingContext(ctx))
}
