package repository

import (
	"context"
	"database/sql"
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
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
	return postgres.DiscardAdvisoryLockConnection(lockConn)
}
