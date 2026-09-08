package repository

import (
	"context"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func applyEvidenceSupersessions(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID string, evidence []EvidenceFragment) error {
	return knowledgepostgres.ApplyEvidenceSupersessionsTx(ctx, knowledgepostgres.LegacyTransaction(tx), input, ingestID, evidence)
}
