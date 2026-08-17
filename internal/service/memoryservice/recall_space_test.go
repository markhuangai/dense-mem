package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRecallFusesAuthorizedSpacesWithLabelsAndStablePrivateTieBreak(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{contract: &repository.ActiveSearchContract{EmbeddingDimensions: 3, EmbeddingModel: ""}}
	svc := NewRecallService(RecallDependencies{Search: search})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "space query", RelationshipLimit: intPtr(0)})
	require.NoError(t, err)
	require.Len(t, result.Results, 2)
	require.Equal(t, string(domain.MemorySpaceCredentialPrivate), result.Results[0].SpaceKind)
	require.Equal(t, string(domain.MemorySpaceTeamShared), result.Results[1].SpaceKind)
	require.Equal(t, privateID.String(), search.inputs[0].SpaceID)
	require.Equal(t, sharedID.String(), search.inputs[1].SpaceID)
}

func TestRecallAcrossSpacesEmbedsQueryOnceAndReusesVector(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{contract: &repository.ActiveSearchContract{EmbeddingDimensions: 3, EmbeddingModel: "recall-model"}}
	provider := &recallEmbeddingProviderStub{}
	svc := NewRecallService(RecallDependencies{Search: search, Provider: provider})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	_, err := svc.Recall(ctx, RecallRequest{Query: "same vector", RelationshipLimit: intPtr(0)})
	require.NoError(t, err)
	require.Equal(t, 1, provider.calls)
	require.Len(t, search.inputs, 2)
	require.Equal(t, []float32{1, 2, 3}, search.inputs[0].QueryEmbedding)
	require.Equal(t, search.inputs[0].QueryEmbedding, search.inputs[1].QueryEmbedding)
}

func TestFuseRecallResultsSumsRRFAndHonorsGlobalLimits(t *testing.T) {
	shared := &RecallResult{
		SearchStates: RecallSearchStates{Evidence: string(domain.SearchProjectionPending), Relationships: string(domain.SearchProjectionCurrent)},
		Results: []RecallResultItem{
			{EvidenceID: "shared-1", Rank: 2, SpaceKind: string(domain.MemorySpaceTeamShared)},
			{EvidenceID: "shared-2", Rank: 2, SpaceKind: string(domain.MemorySpaceTeamShared)},
		},
		RelatedRelationships: []RelatedRelationshipSummary{
			{RelationshipID: "rel-1", SpaceKind: string(domain.MemorySpaceTeamShared)},
			{RelationshipID: "rel-2", SpaceKind: string(domain.MemorySpaceTeamShared)},
		},
	}
	private := &RecallResult{
		SearchStates: RecallSearchStates{Evidence: string(domain.SearchProjectionFailed), Relationships: string(domain.SearchProjectionPending)},
		Results: []RecallResultItem{
			{EvidenceID: "private-1", Rank: 1, SpaceKind: string(domain.MemorySpaceCredentialPrivate)},
			{EvidenceID: "shared-1", Rank: 1, SpaceKind: string(domain.MemorySpaceTeamShared)},
		},
		RelatedRelationships: []RelatedRelationshipSummary{{RelationshipID: "rel-1", SpaceKind: string(domain.MemorySpaceTeamShared)}},
	}

	fused := fuseRecallResults([]*RecallResult{shared, private}, 2, 1)
	require.Len(t, fused.Results, 2)
	require.Equal(t, "shared-1", fused.Results[0].EvidenceID)
	require.Equal(t, "private-1", fused.Results[1].EvidenceID)
	require.Len(t, fused.RelatedRelationships, 1)
	require.Equal(t, "rel-1", fused.RelatedRelationships[0].RelationshipID)
	require.Equal(t, string(domain.SearchProjectionFailed), fused.SearchStates.Evidence)
	require.Equal(t, string(domain.SearchProjectionPending), fused.SearchStates.Relationships)
	require.Equal(t, string(domain.SearchProjectionFailed), fused.SearchState)
}

func TestFuseRecallResultsSkipsNilAndClampsLimits(t *testing.T) {
	empty := fuseRecallResults([]*RecallResult{nil}, 1, 1)
	require.Empty(t, empty.Results)
	require.Empty(t, empty.RelatedRelationships)

	negative := fuseRecallResults([]*RecallResult{{}}, -1, -1)
	require.Empty(t, negative.Results)
	require.Empty(t, negative.RelatedRelationships)
}

func TestRecallPrivateBranchDoesNotExpandTeamGraph(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{contract: &repository.ActiveSearchContract{EmbeddingDimensions: 3}}
	communities := &recallCommunitySnapshotStub{
		records: []repository.CommunityRecallRecord{{
			CommunityID: uuid.NewString(),
			Relationships: []repository.RecallRelationshipHit{{
				RelationshipID: uuid.NewString(),
			}},
		}},
	}
	communityLimit := 1
	svc := NewRecallService(RecallDependencies{
		Search: search, Communities: communities,
		CommunityConfig: recallCommunityConfigStub{enabled: true},
	})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{
		Query: "space query", Limit: 2, RelationshipLimit: intPtr(0), CommunityLimit: &communityLimit,
	})
	require.NoError(t, err)
	require.Len(t, result.RelatedCommunities, 1, "private branch must not repeat team-wide community expansion")
}

func TestRecallPrivateBranchFailureIsBounded(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{
		contract:    &repository.ActiveSearchContract{EmbeddingDimensions: 3},
		failSpaceID: privateID.String(),
	}
	svc := NewRecallService(RecallDependencies{Search: search})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "space query", Limit: 1, RelationshipLimit: intPtr(0)})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Contains(t, result.Degradations, RecallDegradationResult{
		Frontier: "credential_private", Optional: true, Code: "space_branch_unavailable", Message: "authorized memory-space branch was unavailable",
	})
}

func TestRecallSharedBranchFailureRemainsRequired(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{
		contract:    &repository.ActiveSearchContract{EmbeddingDimensions: 3},
		failSpaceID: sharedID.String(),
	}
	svc := NewRecallService(RecallDependencies{Search: search})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "space query", RelationshipLimit: intPtr(0)})
	require.Error(t, err)
	require.Nil(t, result)
}

func TestApplyRecallSpaceKindLabelsNestedCommunityRelationships(t *testing.T) {
	result := &RecallResult{
		Results:              []RecallResultItem{{EvidenceID: "e1"}},
		RelatedRelationships: []RelatedRelationshipSummary{{RelationshipID: "r1"}},
		RelatedCommunities:   []RecallDiscoveryPath{{CommunityRelationships: []RelatedRelationshipSummary{{RelationshipID: "r2"}}}},
	}
	applyRecallSpaceKind(result, "")
	require.Equal(t, string(domain.MemorySpaceTeamShared), result.Results[0].SpaceKind)
	require.Equal(t, string(domain.MemorySpaceTeamShared), result.RelatedRelationships[0].SpaceKind)
	require.Equal(t, string(domain.MemorySpaceTeamShared), result.RelatedCommunities[0].CommunityRelationships[0].SpaceKind)
	applyRecallSpaceKind(nil, string(domain.MemorySpaceTeamShared))
}

func TestFuseRecallSearchStateKeepsEmptyAndCurrentStates(t *testing.T) {
	require.Equal(t, string(domain.SearchProjectionPending), fuseRecallSearchState("", string(domain.SearchProjectionPending)))
	require.Equal(t, string(domain.SearchProjectionCurrent), fuseRecallSearchState(string(domain.SearchProjectionCurrent), ""))
}

func intPtr(v int) *int { return &v }

type spaceRecallStub struct {
	contract    *repository.ActiveSearchContract
	inputs      []repository.RecallEvidenceInput
	failSpaceID string
}

type recallEmbeddingProviderStub struct {
	calls int
}

func (p *recallEmbeddingProviderStub) Embed(context.Context, string) ([]float32, string, error) {
	p.calls++
	return []float32{1, 2, 3}, "recall-model", nil
}

func (*recallEmbeddingProviderStub) EmbedBatch(context.Context, []string) ([][]float32, string, error) {
	return nil, "recall-model", nil
}

func (*recallEmbeddingProviderStub) ModelName() string { return "recall-model" }

func (*recallEmbeddingProviderStub) Dimensions() int { return 3 }

func (*recallEmbeddingProviderStub) IsAvailable() bool { return true }

func (s *spaceRecallStub) GetActiveSearchContract(context.Context) (*repository.ActiveSearchContract, error) {
	return s.contract, nil
}

func (s *spaceRecallStub) RecallEvidence(_ context.Context, input repository.RecallEvidenceInput) (*repository.RecallEvidenceResult, error) {
	s.inputs = append(s.inputs, input)
	if input.SpaceID == s.failSpaceID {
		return nil, errors.New("space branch unavailable")
	}
	id := uuid.NewString()
	return &repository.RecallEvidenceResult{TeamID: input.TeamID, SearchState: string(domain.SearchProjectionCurrent), Results: []repository.RecallEvidenceHit{{EvidenceID: id, Rank: 1, Context: input.SpaceID}}}, nil
}

func (s *spaceRecallStub) RecallRelationships(_ context.Context, input repository.RecallRelationshipsInput) (*repository.RecallRelationshipsResult, error) {
	return &repository.RecallRelationshipsResult{TeamID: input.TeamID, SearchState: string(domain.SearchProjectionCurrent), Results: []repository.RecallRelationshipHit{}}, nil
}
