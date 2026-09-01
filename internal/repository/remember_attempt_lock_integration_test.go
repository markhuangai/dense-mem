package repository

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRememberIdempotencyLockFansOutSameKeyWaiters(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewLedgerRepository(appDB, nil)
	teamID, ownerID, key := uuid.NewString(), uuid.NewString(), "remember-lock-fanout"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	wantErr := errors.New("callback failed")
	var callbackCalls atomic.Int32
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- repo.WithRememberIdempotencyLock(ctx, teamID, ownerID, key, func() error {
			callbackCalls.Add(1)
			close(entered)
			<-release
			return wantErr
		})
	}()
	<-entered

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- repo.WithRememberIdempotencyLock(ctx, teamID, ownerID, key, func() error {
			callbackCalls.Add(1)
			return nil
		})
	}()
	select {
	case err := <-secondErr:
		t.Fatalf("same-key waiter returned before owner completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.ErrorIs(t, <-firstErr, wantErr)
	require.ErrorIs(t, <-secondErr, wantErr)
	require.EqualValues(t, 1, callbackCalls.Load())
}

func TestRememberIdempotencyLockSerializesAcrossRepositoryInstances(t *testing.T) {
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
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- firstRepo.WithRememberIdempotencyLock(ctx, teamID, ownerID, key, func() error {
			close(firstEntered)
			<-release
			return nil
		})
	}()
	<-firstEntered

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- secondRepo.WithRememberIdempotencyLock(ctx, teamID, ownerID, key, func() error {
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
	require.NoError(t, <-secondErr)
}

func TestRememberIdempotencyLockAllowsDifferentKeysConcurrently(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(3)
	sqlDB.SetMaxIdleConns(3)
	repo := NewLedgerRepository(appDB, nil)
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	errorsCh := make(chan error, 2)
	for index := 0; index < 2; index++ {
		key := "remember-lock-different-" + uuid.NewString()
		go func() {
			errorsCh <- repo.WithRememberIdempotencyLock(ctx, teamID, ownerID, key, func() error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("different-key callback did not start concurrently")
		}
	}
	close(release)
	for range 2 {
		require.NoError(t, <-errorsCh)
	}
}

func TestRememberIdempotencyLockCancellationDiscardsConnection(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(3)
	sqlDB.SetMaxIdleConns(3)
	teamID, ownerID, key := uuid.NewString(), uuid.NewString(), "remember-lock-cancel"
	lockKey := rememberIdempotencyLockNamespace + teamID + ":" + ownerID + ":" + key
	externalCtx, externalCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer externalCancel()
	external, err := sqlDB.Conn(externalCtx)
	require.NoError(t, err)
	defer external.Close()
	_, err = external.ExecContext(externalCtx,
		"SELECT pg_advisory_lock(hashtextextended($1, $2))", lockKey, rememberIdempotencyLockHashSeed,
	)
	require.NoError(t, err)

	lockCtx, lockCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer lockCancel()
	repo := NewLedgerRepository(appDB, nil)
	called := false
	err = repo.WithRememberIdempotencyLock(lockCtx, teamID, ownerID, key, func() error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, called)
	_, err = external.ExecContext(externalCtx,
		"SELECT pg_advisory_unlock(hashtextextended($1, $2))", lockKey, rememberIdempotencyLockHashSeed,
	)
	require.NoError(t, err)
	require.NoError(t, repo.WithRememberIdempotencyLock(externalCtx, teamID, ownerID, key, func() error { return nil }))
}

func TestRememberIdempotencyLockDoesNotConsumeDreamAdmission(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(dreamConfirmationLockAdmissionLimit + sharedAdvisoryLockAdmissionLimit + 1)
	sqlDB.SetMaxIdleConns(dreamConfirmationLockAdmissionLimit + sharedAdvisoryLockAdmissionLimit + 1)
	teamID := createLedgerTeam(t, adminDB, rls, "remember-lock-dream-admission")
	rememberRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	entered := make(chan struct{}, sharedAdvisoryLockAdmissionLimit)
	rememberErrors := make(chan error, sharedAdvisoryLockAdmissionLimit)
	for index := 0; index < sharedAdvisoryLockAdmissionLimit; index++ {
		key := "remember-lock-dream-" + uuid.NewString()
		go func() {
			rememberErrors <- rememberRepo.WithRememberIdempotencyLock(ctx, teamID, uuid.NewString(), key, func() error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for range sharedAdvisoryLockAdmissionLimit {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("Remember lock did not consume its bounded admission")
		}
	}

	dreamEntered := make(chan struct{})
	dreamErr := make(chan error, 1)
	go func() {
		dreamErr <- semanticRepo.WithHypothesisConfirmationLock(ctx, teamID, uuid.NewString(), func(DreamRepository) error {
			close(dreamEntered)
			return nil
		})
	}()
	select {
	case <-dreamEntered:
	case err := <-dreamErr:
		t.Fatalf("Dream admission changed by Remember locks: %v", err)
	case <-ctx.Done():
		t.Fatal("Dream admission did not reach its callback")
	}
	require.NoError(t, <-dreamErr)
	close(release)
	for range sharedAdvisoryLockAdmissionLimit {
		require.NoError(t, <-rememberErrors)
	}
}

func TestRememberIdempotencyLockDiscardHelperClosesBadConnection(t *testing.T) {
	_, appDB, _, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	sqlDB, err := appDB.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, discardAdvisoryLockConnection(conn))
	require.ErrorIs(t, conn.PingContext(ctx), sql.ErrConnDone)
	require.NoError(t, sqlDB.PingContext(ctx))
}

func TestRememberIdempotencyLockDifferentKeysUseIndependentCallbacks(t *testing.T) {
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
			errorsCh <- repo.WithRememberAttemptLock(ctx, teamID, ownerID, uuid.NewString(), func() error { return nil })
		}()
	}
	group.Wait()
	for range 2 {
		require.NoError(t, <-errorsCh)
	}
}
