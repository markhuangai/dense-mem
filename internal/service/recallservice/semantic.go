package recallservice

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type SemanticRecallStore interface {
	SearchRecallLexicalCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error)
	SearchRecallVectorCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error)
	SearchRecallAdjacencyCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope, seeds []domain.SemanticRecallEntitySeed) ([]domain.SemanticRecallCandidate, error)
	HydrateRecallEvidence(ctx context.Context, scope domain.SemanticRecallSearchScope, evidenceIDs, preferredRelationshipIDs []string) ([]domain.SemanticRecallResult, error)
}

type semanticRecallService struct {
	store       SemanticRecallStore
	embedder    EmbeddingProvider
	vectorSlots chan struct{}
	ranking     SemanticRecallRankingProfile
}

type semanticRecallBranchResult struct {
	batch               domain.SemanticRecallCandidateBatch
	embedding           []float32
	embeddingContractID string
	err                 error
}

var _ RecallService = (*semanticRecallService)(nil)

const (
	semanticVectorConcurrency              = 4
	semanticRecallHydrationMargin          = 10
	semanticRecallBranchLimitFloor         = 60
	semanticRecallBranchLimitMax           = 200
	semanticRecallBranchLimitMultiplier    = 6
	semanticRecallRelationshipsPerEvidence = 2
	semanticRecallDiscoveryCandidateLimit  = 10
)
