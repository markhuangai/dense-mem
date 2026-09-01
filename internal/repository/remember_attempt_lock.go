package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	rememberIdempotencyLockNamespace            = "dense-mem:remember-session:v1:"
	rememberIdempotencyLockHashSeed       int64 = 317
	rememberIdempotencyLockPollInterval         = 25 * time.Millisecond
	rememberIdempotencyLockCleanupTimeout       = 5 * time.Second
)

var ErrRememberIdempotencyBusy = errors.New("remember idempotency key is busy")

var errRememberIdempotencyCallbackPanic = errors.New("remember idempotency lock callback panicked")

type rememberIdempotencyLockEntry struct {
	ready chan struct{}
	err   error
}

// WithRememberIdempotencyLock serializes one request owner for a scoped
// Remember idempotency key. A local same-key waiter shares the owner's result
// and never consumes another PostgreSQL session; callers that need replay can
// load the durable attempt after this method returns.
func (r *LedgerRepositoryImpl) WithRememberIdempotencyLock(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	idempotencyKey string,
	fn func() error,
) error {
	if fn == nil {
		return errors.New("remember idempotency lock: callback is required")
	}
	return r.withRememberIdempotencyLock(ctx, teamID, ownerProfileID, idempotencyKey, func(bool) error {
		return fn()
	})
}

func (r *LedgerRepositoryImpl) withRememberIdempotencyLock(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	idempotencyKey string,
	fn func(bool) error,
) error {
	if r == nil || r.db == nil {
		return errors.New("remember idempotency lock: database is required")
	}
	teamID = strings.TrimSpace(teamID)
	ownerProfileID = strings.TrimSpace(ownerProfileID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("remember idempotency lock: team_id is required: %w", err)
	}
	if _, err := uuid.Parse(ownerProfileID); err != nil {
		return fmt.Errorf("remember idempotency lock: owner_profile_id is required: %w", err)
	}
	if idempotencyKey == "" {
		return errors.New("remember idempotency lock: idempotency_key is required")
	}
	if fn == nil {
		return errors.New("remember idempotency lock: callback is required")
	}
	localKey := rememberIdempotencyLockNamespace + teamID + ":" + ownerProfileID + ":" + idempotencyKey

	r.rememberIdempotencyLockMu.Lock()
	if existing, ok := r.rememberIdempotencyLocks[localKey]; ok {
		ready := existing.ready
		r.rememberIdempotencyLockMu.Unlock()
		select {
		case <-ready:
			r.rememberIdempotencyLockMu.Lock()
			err := existing.err
			r.rememberIdempotencyLockMu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	entry := &rememberIdempotencyLockEntry{ready: make(chan struct{})}
	if r.rememberIdempotencyLocks == nil {
		r.rememberIdempotencyLocks = make(map[string]*rememberIdempotencyLockEntry)
	}
	r.rememberIdempotencyLocks[localKey] = entry
	r.rememberIdempotencyLockMu.Unlock()

	finish := func(err error) {
		r.rememberIdempotencyLockMu.Lock()
		entry.err = err
		delete(r.rememberIdempotencyLocks, localKey)
		close(entry.ready)
		r.rememberIdempotencyLockMu.Unlock()
	}
	releaseAdmission, err := acquireSharedAdvisoryLockAdmission(ctx, r.db, sharedAdvisoryLockAdmissionLimit)
	if err != nil {
		if errors.Is(err, errAdvisoryLockAdmissionBusy) {
			err = ErrRememberIdempotencyBusy
		}
		finish(err)
		return err
	}
	defer releaseAdmission()
	appDB, err := r.db.DB()
	if err != nil {
		finish(fmt.Errorf("remember idempotency lock: application database handle: %w", err))
		return err
	}
	lockConn, err := appDB.Conn(ctx)
	if err != nil {
		finish(fmt.Errorf("remember idempotency lock: acquire application connection: %w", err))
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = lockConn.Close()
		}
	}()
	acquired, waited, err := acquireRememberSessionAdvisoryLock(ctx, lockConn, localKey)
	if err != nil {
		if discardErr := discardAdvisoryLockConnection(lockConn); discardErr != nil {
			err = errors.Join(err, discardErr)
		}
		closed = true
		finish(err)
		return err
	}
	if !acquired {
		err = ErrRememberIdempotencyBusy
		_ = lockConn.Close()
		closed = true
		finish(err)
		return err
	}

	callbackReturned := false
	defer func() {
		if callbackReturned {
			return
		}
		panicValue := recover()
		cleanupErr := discardAdvisoryLockConnection(lockConn)
		closed = true
		finish(errors.Join(errRememberIdempotencyCallbackPanic, cleanupErr))
		panic(panicValue)
	}()
	callbackErr := fn(waited)
	callbackReturned = true
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rememberIdempotencyLockCleanupTimeout)
	var released bool
	releaseErr := lockConn.QueryRowContext(releaseCtx,
		"SELECT pg_advisory_unlock(hashtextextended($1, $2))", localKey, rememberIdempotencyLockHashSeed,
	).Scan(&released)
	cancel()
	if releaseErr == nil && !released {
		releaseErr = errors.New("database did not release the Remember advisory lock")
	}
	var closeErr error
	if releaseErr != nil {
		closeErr = discardAdvisoryLockConnection(lockConn)
		closed = true
	} else {
		closeErr = lockConn.Close()
		closed = true
	}
	if releaseErr != nil {
		releaseErr = fmt.Errorf("remember idempotency lock release: %w", releaseErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("remember idempotency lock connection close: %w", closeErr)
	}
	err = errors.Join(callbackErr, releaseErr, closeErr)
	finish(err)
	return err
}

func acquireRememberSessionAdvisoryLock(ctx context.Context, conn *sql.Conn, key string) (bool, bool, error) {
	waited := false
	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx,
			"SELECT pg_try_advisory_lock(hashtextextended($1, $2))", key, rememberIdempotencyLockHashSeed,
		).Scan(&acquired); err != nil {
			return false, waited, err
		}
		if acquired {
			return true, waited, nil
		}
		waited = true
		timer := time.NewTimer(rememberIdempotencyLockPollInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, waited, ctx.Err()
		}
	}
}

func discardAdvisoryLockConnection(lockConn *sql.Conn) error {
	if lockConn == nil {
		return nil
	}
	err := lockConn.Raw(func(any) error {
		return driver.ErrBadConn
	})
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("advisory lock discard: %w", err)
	}
	return errors.New("advisory lock discard: connection was not discarded")
}

// WithRememberAttemptLock is the attempt-aware lock entry point. The callback
// receives true when this caller waited for an existing distributed owner;
// such callers must replay the owner's durable result instead of processing.
func (r *LedgerRepositoryImpl) WithRememberAttemptLock(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	idempotencyKey string,
	fn func(waited bool) error,
) error {
	return r.withRememberIdempotencyLock(ctx, teamID, ownerProfileID, idempotencyKey, fn)
}
