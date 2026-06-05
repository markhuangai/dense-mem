package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/observability"
	"gorm.io/gorm"
)

// ClaimLocker is the companion interface for advisory lock claim serialization.
// Downstream units MUST depend on this interface rather than the concrete *ClaimLock
// type so that unit tests can inject a stub.
type ClaimLocker interface {
	// WithClaimLock acquires a Postgres advisory transaction lock scoped to
	// profileID+claimID before invoking fn inside the same SQL transaction.
	// The lock is bounded by timeout. Lock-wait duration is emitted via the
	// injected metrics observer as promote_lock_wait_seconds.
	//
	// Profile isolation invariant: profileID is embedded in the lock key so
	// concurrent promotions of the same claimID in different profiles never
	// contend on the same Postgres advisory lock.
	WithClaimLock(ctx context.Context, db *gorm.DB, profileID, claimID string, timeout time.Duration, fn func(tx *gorm.DB) error) error
}

// ClaimLock implements ClaimLocker using Postgres advisory transaction locks.
// Advisory locks are automatically released when the enclosing transaction
// commits or rolls back, so there is no manual unlock step.
type ClaimLock struct {
	metrics observability.DiscoverabilityMetrics
}

// Compile-time interface satisfaction check.
var _ ClaimLocker = (*ClaimLock)(nil)

// NewClaimLock creates a ClaimLock helper that emits lock-wait durations via
// the provided metrics observer. If metrics is nil, a no-op observer is used.
func NewClaimLock(metrics observability.DiscoverabilityMetrics) *ClaimLock {
	if metrics == nil {
		metrics = observability.NoopDiscoverabilityMetrics()
	}
	return &ClaimLock{metrics: metrics}
}

// claimLockKey returns the advisory-lock input string for a given profile+claim scope.
// The string is passed through Postgres hashtext() to convert it to an int4 lock key.
//
// Profile isolation: profileID is always embedded so that claim-1 in profile-A
// and claim-1 in profile-B hash to different int4 values and never contend.
func claimLockKey(profileID, claimID string) string {
	return "claim:" + profileID + ":" + claimID
}

// WithClaimLock acquires a Postgres advisory transaction lock scoped to
// profileID+claimID, then invokes fn inside the same SQL transaction.
//
// Lock acquisition uses:
//
//	SELECT pg_advisory_xact_lock(hashtext($1))
//
// where $1 = "claim:<profileID>:<claimID>". The lock is automatically released
// when the enclosing transaction commits or rolls back.
//
// The combined timeout (lock-wait + fn execution) is bounded by timeout, which
// should be sourced from CONFIG_PROMOTE_TX_TIMEOUT_SEC by the caller. Lock-wait
// duration is measured and emitted as promote_lock_wait_seconds via the injected
// metrics observer.
//
// Callers must not commit or rollback the tx passed to fn — the transaction
// lifecycle is fully managed by WithClaimLock.
func (c *ClaimLock) WithClaimLock(
	ctx context.Context,
	db *gorm.DB,
	profileID, claimID string,
	timeout time.Duration,
	fn func(tx *gorm.DB) error,
) error {
	lockCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lockWaitStart := time.Now()
	lockObserved := false

	return db.WithContext(lockCtx).Transaction(func(tx *gorm.DB) error {
		// Acquire the advisory transaction lock.
		// Parameter binding prevents SQL injection: profileID and claimID are
		// concatenated in Go, not in SQL, and passed as a single bound parameter.
		key := claimLockKey(profileID, claimID)
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
			return fmt.Errorf("advisory lock acquire failed (profile=%s claim=%s): %w",
				profileID, claimID, err)
		}

		// Measure lock-wait duration once per transaction.
		if !lockObserved {
			lockObserved = true
			observability.RecordPromoteLockWait(ctx, c.metrics, time.Since(lockWaitStart).Seconds())
		}

		return fn(tx)
	})
}

// WithClaimLock is a package-level convenience function that creates a ClaimLock
// with a no-op metrics observer and immediately acquires the advisory lock.
//
// Callers that need to record promote_lock_wait_seconds should use
// NewClaimLock(metrics).WithClaimLock(...) instead.
func WithClaimLock(
	ctx context.Context,
	db *gorm.DB,
	profileID, claimID string,
	timeout time.Duration,
	fn func(tx *gorm.DB) error,
) error {
	return NewClaimLock(nil).WithClaimLock(ctx, db, profileID, claimID, timeout, fn)
}
