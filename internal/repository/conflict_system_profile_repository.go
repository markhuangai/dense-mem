package repository

import (
	"context"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func ensureConflictSystemProfile(ctx context.Context, tx *gorm.DB, teamID string) (string, error) {
	return knowledgepostgres.EnsureConflictSystemProfile(ctx, knowledgepostgres.LegacyTransaction(tx), teamID)
}
