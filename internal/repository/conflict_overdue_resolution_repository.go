package repository

import (
	"context"
	"errors"
)

func (r *LedgerRepositoryImpl) ResumePendingOverdueConflictResolution(
	ctx context.Context,
	input ResumePendingOverdueConflictResolutionInput,
) (*RelationshipConflictResolutionInput, bool, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, false, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.ResumePendingOverdueConflictResolution(ctx, input)
}
