package repository

import (
	"context"
)

// PlanRelationshipCorrectionEmbeddings renders the exact provider batch in
// the PostgreSQL owner. The repository method remains a single-hop facade.
func (r *SemanticRepositoryImpl) PlanRelationshipCorrectionEmbeddings(ctx context.Context, input CorrectRelationshipInput) (*RelationshipCorrectionEmbeddingPlan, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.PlanRelationshipCorrectionEmbeddings(ctx, toKnowledgeCorrectRelationshipInput(input))
	return fromKnowledgeRelationshipCorrectionEmbeddingPlan(result), err
}
