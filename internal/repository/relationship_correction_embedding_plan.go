package repository

import (
	"context"
	"errors"
)

// PlanRelationshipCorrectionEmbeddings renders the exact provider batch in
// the PostgreSQL owner. The repository method remains a single-hop facade.
func (r *SemanticRepositoryImpl) PlanRelationshipCorrectionEmbeddings(ctx context.Context, input CorrectRelationshipInput) (*RelationshipCorrectionEmbeddingPlan, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("semantic: knowledge write owner is required")
	}
	result, err := owner.PlanRelationshipCorrectionEmbeddings(ctx, toKnowledgeCorrectRelationshipInput(input))
	return fromKnowledgeRelationshipCorrectionEmbeddingPlan(result), err
}
