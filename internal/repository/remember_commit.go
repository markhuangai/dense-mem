package repository

import (
	"context"
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// SynchronousRememberCommitResult is the terminal, replayable result produced by
// the one request-owned Remember transaction. PublicResult is already safe to
// persist and replay; callers must not reconstruct it from placement rows.
type SynchronousRememberCommitResult = knowledgecontract.SynchronousRememberCommitResult

// RememberCommitFailureStage preserves the legacy diagnostic helper while
// keeping the owner-generated error classification authoritative.
func RememberCommitFailureStage(err error) string {
	return knowledgepostgres.RememberCommitFailureStage(err)
}

// CommitRememberWithEmbeddings persists one accepted Remember after
// provider work has completed. No knowledge-ingest, evidence, placement, or
// attempt row exists before this transaction starts.
func (r *LedgerRepositoryImpl) CommitRememberWithEmbeddings(ctx context.Context, input SynchronousRememberCommitInput, embeddings []InlineEmbeddingResult) (*SynchronousRememberCommitResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.CommitRememberWithEmbeddings(ctx, toKnowledgeSynchronousRememberCommitInput(input), embeddings)
}
