package repository

import (
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func NewLedgerRepository(db *gorm.DB, rls *postgres.RLS) *LedgerRepositoryImpl {
	return NewLedgerRepositoryWithRuntimeConfig(db, rls, ConflictRuntimeConfig{})
}

func NewLedgerRepositoryWithRuntimeConfig(
	db *gorm.DB,
	rls *postgres.RLS,
	conflictConfig ConflictRuntimeConfig,
) *LedgerRepositoryImpl {
	conflictConfig = normalizeConflictRuntimeConfig(conflictConfig)
	return &LedgerRepositoryImpl{
		db:                     db,
		rls:                    rls,
		conflictReviewTTLDays:  conflictConfig.ReviewTTLDays,
		conflictReviewTimezone: conflictConfig.Timezone,
	}
}
