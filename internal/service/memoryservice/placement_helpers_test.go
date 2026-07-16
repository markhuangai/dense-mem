package memoryservice

import (
	"io"
	"log/slog"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestPlacementLogUsesConfiguredLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(Dependencies{Logger: logger})

	require.Same(t, logger, svc.log())
	require.NotNil(t, New(Dependencies{}).log())
}

func TestExtractServerClaimAndFalseEvidenceHelpers(t *testing.T) {
	claim, ok := extractServerClaim(domain.MemoryEvidence{
		Content:        "Dense-Mem uses Postgres.",
		IdempotencyKey: "idem-1",
	}, "fragment-1")
	require.True(t, ok)
	require.Equal(t, "Dense-Mem", claim.Subject)
	require.Equal(t, "uses", claim.Predicate)
	require.Equal(t, "Postgres", claim.Object)
	require.Equal(t, []string{"fragment-1"}, claim.SupportedBy)
	require.Equal(t, "idem-1", claim.PipelineRunID)

	_, ok = extractServerClaim(domain.MemoryEvidence{Content: "I prefer ."}, "fragment-empty")
	require.False(t, ok)
	_, ok = extractServerClaim(domain.MemoryEvidence{Content: "No extractable personal claim."}, "fragment-none")
	require.False(t, ok)
	_, ok = extractServerClaim(domain.MemoryEvidence{Content: " "}, "fragment-blank")
	require.False(t, ok)

	require.True(t, evidenceLooksFalse([]EvidenceInput{{Content: "This contradicts the memory."}}))
	require.False(t, evidenceLooksFalse([]EvidenceInput{{Content: "This supports the memory."}}))
}

func TestSemanticPlacementCategory(t *testing.T) {
	require.Equal(t, domain.MemoryPlacementPromotedFact, semanticPlacementCategory(domain.SemanticTierFact))
	require.Equal(t, domain.MemoryPlacementValidatedClaim, semanticPlacementCategory(domain.SemanticTierValidatedClaim))
	require.Equal(t, domain.MemoryPlacementCandidateClaim, semanticPlacementCategory(domain.SemanticTierCandidate))
}

func TestSemanticPlacementRelationshipPresentation(t *testing.T) {
	tests := []struct {
		name     string
		rel      domain.SemanticRelationship
		category domain.MemoryPlacementCategory
		reason   string
	}{
		{
			name:     "active fact",
			rel:      domain.SemanticRelationship{Tier: domain.SemanticTierFact, Status: domain.SemanticStatusActive},
			category: domain.MemoryPlacementPromotedFact,
			reason:   "Dense-Mem placed this evidence as an active semantic relationship.",
		},
		{
			name:     "active validated claim",
			rel:      domain.SemanticRelationship{Tier: domain.SemanticTierValidatedClaim, Status: domain.SemanticStatusActive},
			category: domain.MemoryPlacementValidatedClaim,
			reason:   "Dense-Mem placed this evidence as an active semantic relationship.",
		},
		{
			name:     "pending evidence",
			rel:      domain.SemanticRelationship{Tier: domain.SemanticTierCandidate, Status: domain.SemanticStatusPendingEvidence},
			category: domain.MemoryPlacementNeedsEvidence,
			reason:   "Dense-Mem retained this relationship as a candidate pending stronger evidence.",
		},
		{
			name:     "needs review",
			rel:      domain.SemanticRelationship{Tier: domain.SemanticTierCandidate, Status: domain.SemanticStatusNeedsReview},
			category: domain.MemoryPlacementCandidateClaim,
			reason:   "Dense-Mem retained this relationship for review; it is excluded from recall.",
		},
		{
			name:     "rejected",
			rel:      domain.SemanticRelationship{Tier: domain.SemanticTierCandidate, Status: domain.SemanticStatusRejected},
			category: domain.MemoryPlacementRejectedFalse,
			reason:   "Dense-Mem retained the rejected relationship for audit; it is excluded from recall.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.category, semanticPlacementRelationshipCategory(tt.rel))
			require.Equal(t, tt.reason, semanticPlacementRelationshipReason(tt.rel))
		})
	}
}

func TestSemanticPlacementRelationshipOutcomes(t *testing.T) {
	stored := &repository.SemanticRememberResult{
		Relationships: []domain.SemanticRelationship{
			{
				RelationshipID: "rel-fact",
				OwnerProfileID: "owner-1",
				Tier:           domain.SemanticTierFact,
				Status:         domain.SemanticStatusActive,
			},
			{
				RelationshipID: "rel-rejected",
				OwnerProfileID: "owner-2",
				Tier:           domain.SemanticTierCandidate,
				Status:         domain.SemanticStatusRejected,
			},
		},
		RelationshipInputIndexes: []int{0, 4},
	}
	relationships := []repository.SemanticRelationshipInput{
		{EvidenceIndex: 0},
		{
			EvidenceIndex:            1,
			PlacementOutcomeCategory: "identity_needs_review",
			PlacementReviewMessage:   "identity ambiguous",
		},
		{
			EvidenceIndex:            2,
			PlacementOutcomeCategory: "predicate_needs_review",
		},
		{
			EvidenceIndex:            3,
			PlacementOutcomeCategory: "unexpected",
			PlacementReviewMessage:   "needs review",
		},
		{EvidenceIndex: -1},
	}

	outcomes := semanticPlacementRelationshipOutcomes(relationships, stored)

	require.Len(t, outcomes, 4)
	require.Equal(t, "relationship_fact", outcomes[0][0].Category)
	require.Equal(t, "rel-fact", outcomes[0][0].RelationshipID)
	require.Equal(t, domain.SemanticTierFact, outcomes[0][0].Tier)
	require.Equal(t, domain.SemanticStatusActive, outcomes[0][0].RelationshipStatus)

	require.Equal(t, "identity_needs_review", outcomes[1][0].Category)
	require.Equal(t, "identity ambiguous", outcomes[1][0].Reason)
	require.NotNil(t, outcomes[1][0].ReviewTask)
	require.Equal(t, "identity_needs_review", outcomes[1][0].ReviewTask.Type)

	require.Equal(t, "predicate_needs_review", outcomes[2][0].Category)
	require.Equal(t, "Dense-Mem preserved the observation for review; no canonical relationship was created.", outcomes[2][0].Reason)
	require.NotNil(t, outcomes[2][0].ReviewTask)
	require.Equal(t, "predicate_needs_review", outcomes[2][0].ReviewTask.Type)

	require.Equal(t, "relationship_needs_review", outcomes[3][0].Category)
	require.Equal(t, "needs review", outcomes[3][0].Reason)
	require.NotNil(t, outcomes[3][0].ReviewTask)
	require.Equal(t, "relationship_needs_review", outcomes[3][0].ReviewTask.Type)
	require.NotContains(t, outcomes, -1)

	require.Equal(t, "relationship_pending_evidence", semanticPlacementRelationshipOutcomeCategory(domain.SemanticRelationship{
		Status: domain.SemanticStatusPendingEvidence,
	}))
	require.Equal(t, "relationship_needs_review", semanticPlacementRelationshipOutcomeCategory(domain.SemanticRelationship{
		Status: domain.SemanticStatusNeedsReview,
	}))
	require.Equal(t, "relationship_rejected", semanticPlacementRelationshipOutcomeCategory(domain.SemanticRelationship{
		Status: domain.SemanticStatusRejected,
	}))
	require.Equal(t, "relationship_validated_claim", semanticPlacementRelationshipOutcomeCategory(domain.SemanticRelationship{
		Status: domain.SemanticStatusActive,
		Tier:   domain.SemanticTierValidatedClaim,
	}))
	require.Nil(t, semanticPlacementReviewTask("stored_relationship", "no review needed"))
}
