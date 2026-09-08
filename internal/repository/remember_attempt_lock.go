package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var ErrRememberIdempotencyBusy = knowledgecontract.ErrRememberIdempotencyBusy

func (r *LedgerRepositoryImpl) withRememberIdempotencyLock(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	idempotencyKey string,
	fn func(bool) error,
) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("remember idempotency lock: knowledge write owner is required")
	}
	return owner.WithRememberAttemptLock(ctx, teamID, ownerProfileID, idempotencyKey, fn)
}

// WithRememberAttemptLock is the compatibility entry point for the
// knowledge-owned attempt lock.
func (r *LedgerRepositoryImpl) WithRememberAttemptLock(
	ctx context.Context,
	teamID string,
	ownerProfileID string,
	idempotencyKey string,
	fn func(waited bool) error,
) error {
	return r.withRememberIdempotencyLock(ctx, teamID, ownerProfileID, idempotencyKey, fn)
}

// discardAdvisoryLockConnection is retained for the legacy Dream evidence
// lock, which shares the connection-discard safety primitive.
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
