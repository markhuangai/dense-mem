package repository

import (
	"context"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

var (
	ErrRelationshipCorrectionNotFound            = knowledgecontract.ErrRelationshipCorrectionNotFound
	ErrRelationshipCorrectionConfirmation        = knowledgecontract.ErrRelationshipCorrectionConfirmation
	ErrRelationshipCorrectionConfirmationExpired = knowledgecontract.ErrRelationshipCorrectionConfirmationExpired
	ErrRelationshipCorrectionStateConflict       = knowledgecontract.ErrRelationshipCorrectionStateConflict
)

func (r *SemanticRepositoryImpl) CorrectRelationship(ctx context.Context, input CorrectRelationshipInput) (*CorrectRelationshipResult, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.CorrectRelationship(ctx, toKnowledgeCorrectRelationshipInput(input))
	return fromKnowledgeCorrectRelationshipResult(result), err
}

func (r *SemanticRepositoryImpl) CorrectRelationshipWithEmbeddings(ctx context.Context, input CorrectRelationshipInput, embeddings []RelationshipCorrectionEmbedding) (*CorrectRelationshipResult, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.CorrectRelationshipWithEmbeddings(
		ctx,
		toKnowledgeCorrectRelationshipInput(input),
		toKnowledgeRelationshipCorrectionEmbeddings(embeddings),
	)
	return fromKnowledgeCorrectRelationshipResult(result), err
}

func (r *SemanticRepositoryImpl) GetRelationshipCorrection(ctx context.Context, input GetRelationshipCorrectionInput) (*RelationshipCorrectionStatus, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.GetRelationshipCorrection(ctx, toKnowledgeGetRelationshipCorrectionInput(input))
	return fromKnowledgeCorrectRelationshipResult(result), err
}

// These package-local names are retained for legacy E2E fixtures until the
// repository facade is removed by #382.
func normalizeCorrectRelationshipInput(input CorrectRelationshipInput) CorrectRelationshipInput {
	return fromKnowledgeCorrectRelationshipInput(
		knowledgepostgres.NormalizeCorrectRelationshipInput(toKnowledgeCorrectRelationshipInput(input)),
	)
}

func validateCorrectRelationshipInput(input CorrectRelationshipInput) error {
	return knowledgepostgres.ValidateCorrectRelationshipInput(toKnowledgeCorrectRelationshipInput(input))
}
