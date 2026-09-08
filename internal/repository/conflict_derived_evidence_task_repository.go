package repository

import (
	"context"
	"errors"
)

// StageConflictDerivedEvidence forwards the canonical derived-evidence commit
// to the knowledge PostgreSQL owner.
func (r *LedgerRepositoryImpl) StageConflictDerivedEvidence(
	ctx context.Context,
	target ConflictDerivedEvidenceTarget,
) (*StageConflictDerivedEvidenceResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.StageConflictDerivedEvidence(ctx, target)
}

// RecordConflictDerivedEvidenceFailure forwards the retry-state mutation to
// the knowledge PostgreSQL owner.
func (r *LedgerRepositoryImpl) RecordConflictDerivedEvidenceFailure(
	ctx context.Context,
	target ConflictDerivedEvidenceTarget,
	failureClass string,
) error {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return errors.New("conflict review: knowledge write owner is required")
	}
	return owner.RecordConflictDerivedEvidenceFailure(ctx, target, failureClass)
}

// ClaimConflictDerivedEvidenceTasks forwards task claiming to the knowledge
// PostgreSQL owner.
func (r *LedgerRepositoryImpl) ClaimConflictDerivedEvidenceTasks(
	ctx context.Context,
	input ClaimConflictDerivedEvidenceTasksInput,
) ([]ConflictDerivedEvidenceTarget, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("conflict review: knowledge write owner is required")
	}
	return owner.ClaimConflictDerivedEvidenceTasks(ctx, input)
}
