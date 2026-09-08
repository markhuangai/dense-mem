package repository

import (
	"context"
	"errors"
)

func (r *LedgerRepositoryImpl) ValidateRelationshipConflictContext(ctx context.Context, input ValidateRelationshipConflictContextInput) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("ledger: knowledge write owner is required")
	}
	return owner.ValidateRelationshipConflictContext(ctx, input)
}
