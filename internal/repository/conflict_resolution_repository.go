package repository

import (
	"context"
	"errors"
)

func (r *LedgerRepositoryImpl) PlanRelationshipConflictResolution(
	ctx context.Context,
	input RelationshipConflictResolutionInput,
) (*RelationshipConflictResolutionPlan, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.PlanRelationshipConflictResolution(ctx, input)
}

func (r *LedgerRepositoryImpl) CommitRelationshipConflictResolution(
	ctx context.Context,
	input CommitRelationshipConflictResolutionInput,
) (*ApplyOverdueConflictResolutionResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.CommitRelationshipConflictResolution(ctx, input)
}
