package repository

import (
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func NewLedgerRepository(db *gorm.DB, rls *postgres.RLS) *LedgerRepositoryImpl {
	return NewLedgerRepositoryWithEmbeddingJobMaxAttempts(db, rls, defaultEmbeddingJobMaxAttempts)
}

func NewLedgerRepositoryWithEmbeddingJobMaxAttempts(
	db *gorm.DB,
	rls *postgres.RLS,
	maxAttempts int,
) *LedgerRepositoryImpl {
	return NewLedgerRepositoryWithRuntimeConfig(db, rls, maxAttempts, ConflictRuntimeConfig{})
}

func NewLedgerRepositoryWithRuntimeConfig(
	db *gorm.DB,
	rls *postgres.RLS,
	maxAttempts int,
	conflictConfig ConflictRuntimeConfig,
) *LedgerRepositoryImpl {
	conflictConfig = normalizeConflictRuntimeConfig(conflictConfig)
	return &LedgerRepositoryImpl{
		db:                      db,
		rls:                     rls,
		embeddingJobMaxAttempts: normalizeEmbeddingJobMaxAttempts(maxAttempts),
		conflictReviewTTLDays:   conflictConfig.ReviewTTLDays,
		conflictReviewTimezone:  conflictConfig.Timezone,
	}
}
