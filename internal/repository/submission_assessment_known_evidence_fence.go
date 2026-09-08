package repository

import (
	"context"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func reauthorizeSubmissionKnownEvidence(ctx context.Context, tx *gorm.DB, input CommitSubmissionAssessmentInput) error {
	return knowledgepostgres.ReauthorizeSubmissionKnownEvidence(ctx, knowledgepostgres.LegacyTransaction(tx), toKnowledgeCommitSubmissionAssessmentInput(input))
}
