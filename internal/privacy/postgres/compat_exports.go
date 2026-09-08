package postgres

import (
	"time"

	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

// Store is the canonical PostgreSQL owner for private-memory lifecycle data.
// The implementation type retains its historical name only inside this
// package so legacy repository callers can migrate through one adapter.
type Store = PrivateMemoryRepositoryImpl

var _ privacycontract.PrivateMemoryRepository = (*Store)(nil)

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper) *Store {
	return NewPrivateMemoryRepository(db, rls)
}

const (
	MaximumAttempts   = privateMemoryMaximumAttempts
	AuditMetadataJSON = privateMemoryAuditMetadataJSON
)

func RetryDelay(attemptCount int) time.Duration {
	return privateMemoryRetryDelay(attemptCount)
}

func Hash(parts ...string) string {
	return privateMemoryHash(parts...)
}

func WrapError(operation string, err error) error {
	return wrapPrivateMemoryError(operation, err)
}

func (r *Store) PreparedManifest() []string {
	if r == nil {
		return nil
	}
	r.manifestMu.RLock()
	defer r.manifestMu.RUnlock()
	return append([]string(nil), r.ordered...)
}

func (r *Store) SetNow(now func() time.Time) {
	if r == nil || now == nil {
		return
	}
	r.clockMu.Lock()
	defer r.clockMu.Unlock()
	r.now = now
}
