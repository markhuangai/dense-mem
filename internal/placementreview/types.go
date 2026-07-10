package placementreview

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type Request struct {
	ProfileID string
	Evidence  []domain.MemoryEvidence
	Proposal  domain.MemoryProposal
}

type ReviewedEntity struct {
	Proposal         domain.MemoryEntityProposal
	ResolutionStatus domain.EntityResolutionStatus
	ResolutionConf   float64
}

type ReviewedRelationship struct {
	Proposal          domain.MemoryRelationshipProposal
	Atomic            bool
	Ambiguous         bool
	AuthorityExplicit bool
	ExtractConf       float64
	Rationale         string
}

type Result struct {
	Entities      []ReviewedEntity
	Relationships []ReviewedRelationship
	Model         string
}

type Reviewer interface {
	ReviewGraph(ctx context.Context, req Request) (Result, error)
}
