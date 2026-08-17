package memoryservice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
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
	require.Equal(t, string(domain.SearchProjectionNotRequired), result.SearchStates.Relationships)
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

func TestRecallAcrossSpacesReportsEmbeddingDegradationOnce(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{contract: &repository.ActiveSearchContract{EmbeddingDimensions: 3}}
	svc := NewRecallService(RecallDependencies{
		Search:   search,
		Provider: &recallProviderStub{available: false},
	})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "degraded vector", RelationshipLimit: intPtr(0)})
	require.NoError(t, err)
	var degradationCount int
	for _, degradation := range result.Degradations {
		if degradation.Code == string(domain.ErrorProviderUnavailable) {
			degradationCount++
		}
	}
	require.Equal(t, 1, degradationCount)
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

func TestFuseRecallResultsUsesBestRelationshipSummary(t *testing.T) {
	fused := fuseRecallResults([]*RecallResult{
		{RelatedRelationships: []RelatedRelationshipSummary{
			{RelationshipID: "other", SpaceKind: string(domain.MemorySpaceTeamShared)},
			{RelationshipID: "duplicate", SpaceKind: string(domain.MemorySpaceTeamShared), Object: SemanticObject{Name: "rank-two"}},
		}},
		{RelatedRelationships: []RelatedRelationshipSummary{
			{RelationshipID: "duplicate", SpaceKind: string(domain.MemorySpaceTeamShared), Object: SemanticObject{Name: "rank-one"}},
		}},
	}, 0, 1)

	require.Len(t, fused.RelatedRelationships, 1)
	require.Equal(t, "duplicate", fused.RelatedRelationships[0].RelationshipID)
	require.Equal(t, "rank-one", fused.RelatedRelationships[0].Object.Name)
}

func TestFuseRecallResultsOrdersEqualScoresWithStrictSpaceTieBreak(t *testing.T) {
	fused := fuseRecallResults([]*RecallResult{
		{Results: []RecallResultItem{{EvidenceID: "shared", Rank: 1, SpaceKind: string(domain.MemorySpaceTeamShared)}}},
		{Results: []RecallResultItem{{EvidenceID: "profile", Rank: 1, SpaceKind: string(domain.MemorySpaceProfilePrivate)}}},
		{Results: []RecallResultItem{{EvidenceID: "credential", Rank: 1, SpaceKind: string(domain.MemorySpaceCredentialPrivate)}}},
	}, 3, 0)

	require.Equal(t, []string{"credential", "profile", "shared"}, []string{
		fused.Results[0].EvidenceID,
		fused.Results[1].EvidenceID,
		fused.Results[2].EvidenceID,
	})
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
	metrics := &recallCommunityMetricsStub{InMemoryDiscoverabilityMetrics: observability.NewInMemoryDiscoverabilityMetrics()}
	svc := NewRecallService(RecallDependencies{
		Search: search, Communities: communities,
		CommunityConfig: recallCommunityConfigStub{enabled: true},
		Metrics:         metrics,
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
	require.Equal(t, 1, metrics.communityRecallCalls, "fused recall should record one community metric")
}

func TestRecallSingleSharedSpaceRecordsCommunityMetric(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID := uuid.New()
	search := &spaceRecallStub{contract: &repository.ActiveSearchContract{EmbeddingDimensions: 3}}
	communities := &recallCommunitySnapshotStub{records: []repository.CommunityRecallRecord{{CommunityID: uuid.NewString()}}}
	metrics := &recallCommunityMetricsStub{InMemoryDiscoverabilityMetrics: observability.NewInMemoryDiscoverabilityMetrics()}
	svc := NewRecallService(RecallDependencies{
		Search: search, Communities: communities, Metrics: metrics,
		CommunityConfig: recallCommunityConfigStub{enabled: true},
	})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{{ID: sharedID, Kind: domain.MemorySpaceTeamShared}},
	})

	_, err := svc.Recall(ctx, RecallRequest{Query: "space query", RelationshipLimit: intPtr(0)})
	require.NoError(t, err)
	require.Equal(t, 1, metrics.communityRecallCalls)
}

func TestRecallPrivateBranchFailureIsBounded(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{
		contract:    &repository.ActiveSearchContract{EmbeddingDimensions: 3},
		failSpaceID: privateID.String(),
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	svc := NewRecallService(RecallDependencies{Search: search, Metrics: metrics})
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
		Frontier: "evidence", Optional: true, Code: "space_branch_unavailable", Message: "authorized memory-space branch was unavailable",
	})
	samples := metrics.RecallSamples()
	require.Len(t, samples, 1)
	require.Equal(t, "ok", samples[0].Outcome)
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
	require.Equal(t, string(domain.SearchProjectionPending), domain.CombineSearchProjectionStates("", string(domain.SearchProjectionPending)))
	require.Equal(t, string(domain.SearchProjectionCurrent), domain.CombineSearchProjectionStates(string(domain.SearchProjectionCurrent), ""))
	require.Equal(t, string(domain.SearchProjectionCurrent), domain.CombineSearchProjectionStates(string(domain.SearchProjectionNotRequired), string(domain.SearchProjectionCurrent)))
}

func TestFuseRecallResultsPreservesNotRequiredRelationshipState(t *testing.T) {
	fused := fuseRecallResults([]*RecallResult{
		{SearchStates: RecallSearchStates{Evidence: string(domain.SearchProjectionCurrent), Relationships: string(domain.SearchProjectionNotRequired)}},
		{SearchStates: RecallSearchStates{Evidence: string(domain.SearchProjectionCurrent), Relationships: string(domain.SearchProjectionNotRequired)}},
	}, 1, 0)
	require.Equal(t, string(domain.SearchProjectionNotRequired), fused.SearchStates.Relationships)
}

func TestFuseRecallResultsDeduplicatesIdenticalDegradations(t *testing.T) {
	degradation := RecallDegradationResult{
		Frontier: "relationships",
		Optional: true,
		Code:     "relationship_vector_warming",
		Message:  "Relationship vector projection is warming; lexical discovery was used.",
	}
	fused := fuseRecallResults([]*RecallResult{
		{Degradations: []RecallDegradationResult{degradation}},
		{Degradations: []RecallDegradationResult{degradation}},
	}, 1, 0)
	require.Equal(t, []RecallDegradationResult{degradation}, fused.Degradations)
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

type recallCommunityMetricsStub struct {
	*observability.InMemoryDiscoverabilityMetrics
	communityRecallCalls int
}

func (m *recallCommunityMetricsStub) ObserveCommunityRecall(context.Context, string, int, int) {
	m.communityRecallCalls++
}

func (*recallCommunityMetricsStub) ObserveCommunityRun(context.Context, string, int, int, int) {}

func (*recallCommunityMetricsStub) ObserveCommunitySummary(context.Context, string, int) {}

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
	if s.failSpaceID != "" && input.SpaceID == s.failSpaceID {
		return nil, errors.New("space branch unavailable")
	}
	id := uuid.NewString()
	return &repository.RecallEvidenceResult{TeamID: input.TeamID, SearchState: string(domain.SearchProjectionCurrent), Results: []repository.RecallEvidenceHit{{EvidenceID: id, Rank: 1, Context: input.SpaceID}}}, nil
}

func (s *spaceRecallStub) RecallRelationships(_ context.Context, input repository.RecallRelationshipsInput) (*repository.RecallRelationshipsResult, error) {
	return &repository.RecallRelationshipsResult{TeamID: input.TeamID, SearchState: string(domain.SearchProjectionCurrent), Results: []repository.RecallRelationshipHit{}}, nil
}
