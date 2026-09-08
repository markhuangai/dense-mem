package repository

import (
	"context"
	"errors"
)

func (r *LedgerRepositoryImpl) ReserveRelationshipConflictReviewRun(
	ctx context.Context,
	input ConflictReviewRunInput,
) (*ConflictReviewRunRecord, bool, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, false, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.ReserveRelationshipConflictReviewRun(ctx, input)
}

func (r *LedgerRepositoryImpl) ClaimRelationshipConflictCases(
	ctx context.Context,
	input ClaimRelationshipConflictCasesInput,
) ([]RelationshipConflictCaseRecord, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.ClaimRelationshipConflictCases(ctx, input)
}

func (r *LedgerRepositoryImpl) ReleaseRelationshipConflictCaseClaim(
	ctx context.Context,
	input ReleaseRelationshipConflictCaseClaimInput,
) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("conflict review: knowledge write owner is required")
	}
	return owner.ReleaseRelationshipConflictCaseClaim(ctx, input)
}

func (r *LedgerRepositoryImpl) ReviewRelationshipConflictCase(
	ctx context.Context,
	input ReviewRelationshipConflictCaseInput,
) (*ReviewRelationshipConflictCaseResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.ReviewRelationshipConflictCase(ctx, input)
}

func (r *LedgerRepositoryImpl) CompleteRelationshipConflictReviewRun(
	ctx context.Context,
	input ConflictReviewRunCompleteInput,
) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("conflict review: knowledge write owner is required")
	}
	return owner.CompleteRelationshipConflictReviewRun(ctx, input)
}
