package postgres

import (
	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type RememberPort = knowledgecontract.RememberPort
type LifecyclePort = knowledgecontract.LifecyclePort
type ConflictPort = knowledgecontract.ConflictPort

// NewStore constructs the sole PostgreSQL owner for knowledge writes. The
// database and RLS helper are retained on one instance so idempotency locks,
// runtime policy, and transaction boundaries cannot diverge across callers.
func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper, conflictConfig knowledgecontract.ConflictRuntimeConfig) *Store {
	conflictConfig = normalizeConflictRuntimeConfig(conflictConfig)
	return &Store{
		db:                       db,
		rls:                      rls,
		conflictReviewTTLDays:    conflictConfig.ReviewTTLDays,
		conflictReviewTimezone:   conflictConfig.Timezone,
		rememberIdempotencyLocks: make(map[string]*rememberIdempotencyLockEntry),
	}
}

func New(db *gorm.DB, rls storagepostgres.RLSHelper, conflictConfig knowledgecontract.ConflictRuntimeConfig) *Store {
	return NewStore(db, rls, conflictConfig)
}

var _ knowledgecontract.RememberPort = (*Store)(nil)
var _ knowledgecontract.LifecyclePort = (*Store)(nil)
var _ knowledgecontract.ConflictPort = (*Store)(nil)
