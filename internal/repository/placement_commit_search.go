package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// Search projection helpers used by the reconciliation adapter are forwarded
// to the canonical PostgreSQL owner. No repository-local SQL writer remains.
func upsertSearchDocumentInTx(ctx context.Context, tx *gorm.DB, input UpsertSearchDocumentInput, contract *ActiveSearchContract) (*SearchDocumentResult, error) {
	result, err := knowledgepostgres.UpsertSearchDocumentInTx(
		knowledgepostgres.WithInlineEmbeddingResults(ctx, []knowledgepostgres.InlineEmbeddingResult{}),
		knowledgepostgres.LegacyTransaction(tx),
		knowledgepostgres.UpsertSearchDocumentInput(input),
		contract,
	)
	if errors.Is(err, knowledgepostgres.ErrSearchStaleVersion) {
		return nil, nil
	}
	return result, err
}

func semanticRelationshipSearchText(ctx context.Context, tx *gorm.DB, relationship *RelationshipRecord) (string, error) {
	return knowledgepostgres.SemanticRelationshipSearchText(ctx, knowledgepostgres.LegacyTransaction(tx), toKnowledgeRelationshipRecord(relationship))
}

func relationshipSearchEligible(relationship *RelationshipRecord) bool {
	return knowledgepostgres.RelationshipSearchEligible(toKnowledgeRelationshipRecord(relationship))
}

func relationshipForegroundRecallGenerationID(ctx context.Context, tx *gorm.DB, teamID string) (string, error) {
	return knowledgepostgres.RelationshipForegroundRecallGenerationID(ctx, knowledgepostgres.LegacyTransaction(tx), teamID)
}
