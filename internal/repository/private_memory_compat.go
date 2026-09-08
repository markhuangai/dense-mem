package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// These aliases preserve the legacy repository package contract while the
// privacy capability owns the implementation in internal/privacy/postgres.
type PrivateMemoryRepository = privacycontract.PrivateMemoryRepository
type PrivateMemoryErasureRequest = privacycontract.PrivateMemoryErasureRequest
type PrivateMemoryCredentialRevocationAudit = privacycontract.PrivateMemoryCredentialRevocationAudit
type PrivateMemoryRetentionRequest = privacycontract.PrivateMemoryRetentionRequest

var (
	ErrPrivateMemoryNotFound          = privacycontract.ErrPrivateMemoryNotFound
	ErrPrivateMemoryLegalHold         = privacycontract.ErrPrivateMemoryLegalHold
	ErrPrivateMemoryIdempotency       = privacycontract.ErrPrivateMemoryIdempotency
	ErrPrivateMemoryOperationConflict = privacycontract.ErrPrivateMemoryOperationConflict
	ErrPrivateMemoryManifest          = privacycontract.ErrPrivateMemoryManifest
	ErrPrivateMemoryClaimLost         = privacycontract.ErrPrivateMemoryClaimLost
	ErrPrivateMemoryRetentionDisabled = privacycontract.ErrPrivateMemoryRetentionDisabled
	ErrPrivateMemoryHoldConflict      = privacycontract.ErrPrivateMemoryHoldConflict
	ErrPrivateMemoryInternal          = privacycontract.ErrPrivateMemoryInternal
)

const (
	privateMemoryMaximumAttempts   = privacypostgres.MaximumAttempts
	privateMemoryAuditMetadataJSON = privacypostgres.AuditMetadataJSON
)

// PrivateMemoryRepositoryImpl is a single-hop compatibility facade for
// callers that still construct the legacy repository package.
type PrivateMemoryRepositoryImpl struct {
	*privacypostgres.Store
	ordered []string
	now     func() time.Time
}

var _ PrivateMemoryRepository = (*PrivateMemoryRepositoryImpl)(nil)

func NewPrivateMemoryRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *PrivateMemoryRepositoryImpl {
	return &PrivateMemoryRepositoryImpl{
		Store: privacypostgres.NewStore(db, rls),
		now:   func() time.Time { return time.Now().UTC() },
	}
}

func PrivateMemoryErasureManifest() []string {
	return privacypostgres.PrivateMemoryErasureManifest()
}

func (r *PrivateMemoryRepositoryImpl) Prepare(ctx context.Context) error {
	if r == nil || r.Store == nil {
		return privacypostgres.ErrPrivateMemoryManifest
	}
	err := r.Store.Prepare(ctx)
	if err == nil {
		r.ordered = r.Store.PreparedManifest()
	}
	return err
}

func (r *PrivateMemoryRepositoryImpl) syncClock() {
	if r != nil && r.Store != nil && r.now != nil {
		r.Store.SetNow(r.now)
	}
}

func (r *PrivateMemoryRepositoryImpl) DisableSSOCredential(ctx context.Context, input PrivateMemoryErasureRequest) (*domain.PrivateMemoryErasureOperation, bool, error) {
	r.syncClock()
	return r.Store.DisableSSOCredential(ctx, input)
}

func (r *PrivateMemoryRepositoryImpl) PlaceLegalHold(ctx context.Context, spaceID uuid.UUID, reasonCode string) (*domain.PrivateMemoryLegalHold, bool, error) {
	r.syncClock()
	return r.Store.PlaceLegalHold(ctx, spaceID, reasonCode)
}

func (r *PrivateMemoryRepositoryImpl) ReleaseLegalHold(ctx context.Context, spaceID uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error) {
	r.syncClock()
	return r.Store.ReleaseLegalHold(ctx, spaceID)
}

func (r *PrivateMemoryRepositoryImpl) RunRetention(ctx context.Context, input PrivateMemoryRetentionRequest) (*domain.PrivateMemoryRetentionRun, bool, error) {
	r.syncClock()
	return r.Store.RunRetention(ctx, input)
}

func (r *PrivateMemoryRepositoryImpl) ClaimNext(ctx context.Context, workerID string, lease time.Duration) (*domain.PrivateMemoryErasureOperation, error) {
	r.syncClock()
	return r.Store.ClaimNext(ctx, workerID, lease)
}

func (r *PrivateMemoryRepositoryImpl) ExecuteClaim(ctx context.Context, operationID uuid.UUID, workerID string, fence int64) (*domain.PrivateMemoryErasureOperation, error) {
	r.syncClock()
	return r.Store.ExecuteClaim(ctx, operationID, workerID, fence)
}

func (r *PrivateMemoryRepositoryImpl) ReleaseClaim(ctx context.Context, operationID uuid.UUID, workerID string, fence int64, errorCode string) error {
	r.syncClock()
	return r.Store.ReleaseClaim(ctx, operationID, workerID, fence, errorCode)
}

func privateMemoryHash(parts ...string) string {
	return privacypostgres.Hash(parts...)
}

func privateMemoryRetryDelay(attemptCount int) time.Duration {
	return privacypostgres.RetryDelay(attemptCount)
}

func wrapPrivateMemoryError(operation string, err error) error {
	return privacypostgres.WrapError(operation, err)
}
