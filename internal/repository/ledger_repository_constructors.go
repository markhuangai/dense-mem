package repository

import (
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func normalizeConflictRuntimeConfig(input ConflictRuntimeConfig) ConflictRuntimeConfig {
	return knowledgepostgres.NormalizeConflictRuntimeConfig(input)
}

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
		knowledgeOwner:         knowledgepostgres.NewStore(db, rls, conflictConfig),
	}
}
