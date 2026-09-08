package repository

import (
	"context"
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

const (
	RememberDuplicateCandidateLimit = knowledgepostgres.RememberDuplicateCandidateLimit
	RememberDuplicateMaxEvidence    = knowledgepostgres.RememberDuplicateMaxEvidence
)

var ErrRememberDuplicateCandidateStale = knowledgecontract.ErrRememberDuplicateCandidateStale

type RememberDuplicateCandidate = knowledgecontract.RememberDuplicateCandidate
type RememberDuplicateCandidateGroup = knowledgecontract.RememberDuplicateCandidateGroup
type RememberDuplicateResolution = knowledgecontract.RememberDuplicateResolution
type RememberDuplicateEmbeddingPlan = knowledgecontract.RememberDuplicateEmbeddingPlan
type RememberDuplicateCandidateInput = knowledgecontract.RememberDuplicateCandidateInput
type RememberDuplicateResolutionResult = knowledgecontract.RememberDuplicateResolutionResult

// PlanRememberDuplicateEmbeddings and ResolveRememberDuplicateCandidates are
// compatibility entry points; the knowledge PostgreSQL Store owns their
// transaction and candidate SQL.
func (r *LedgerRepositoryImpl) PlanRememberDuplicateEmbeddings(ctx context.Context, input RememberDuplicateCandidateInput) (*RememberDuplicateEmbeddingPlan, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.PlanRememberDuplicateEmbeddings(ctx, input)
}

func (r *LedgerRepositoryImpl) ResolveRememberDuplicateCandidates(ctx context.Context, input RememberDuplicateCandidateInput, embeddings []InlineEmbeddingResult) (*RememberDuplicateResolutionResult, error) {
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("ledger: knowledge write owner is required")
	}
	return owner.ResolveRememberDuplicateCandidates(ctx, input, embeddings)
}

func validateRememberDuplicateCandidateInput(input RememberDuplicateCandidateInput) error {
	return knowledgepostgres.ValidateRememberDuplicateCandidateInput(input)
}

func rememberDuplicateBatchCanonicalKey(item EvidenceInput) string {
	return knowledgepostgres.RememberDuplicateBatchCanonicalKey(item)
}
