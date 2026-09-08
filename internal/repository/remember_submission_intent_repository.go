package repository

import (
	"context"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// validateRememberSubmissionSupersessionTargets locks and validates the
// lifecycle targets in the same transaction that will apply the accepted
// synchronous Remember commit. No staging intent rows are used.
func validateRememberSubmissionSupersessionTargets(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string) error {
	return knowledgepostgres.ValidateRememberSubmissionSupersessionTargetsTx(ctx, knowledgepostgres.LegacyTransaction(tx), input, ingestID)
}
