package repository

import (
	"context"
	"errors"
)

// PlanRememberEmbeddings renders the exact document batch for a
// request-owned Remember. It performs reads only; no ingest, evidence,
// semantic, search, or attempt row is written before embedding succeeds.
func (r *LedgerRepositoryImpl) PlanRememberEmbeddings(ctx context.Context, input SynchronousRememberCommitInput) (*InlineEmbeddingPlan, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.PlanRememberEmbeddings(ctx, toKnowledgeSynchronousRememberCommitInput(input))
}
