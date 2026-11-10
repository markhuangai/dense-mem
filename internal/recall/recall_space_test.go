package recall

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func TestRecallFusesAuthorizedSpacesWithLabelsAndStablePrivateTieBreak(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{contract: &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3, EmbeddingModel: ""}}
	svc := NewRecallService(RecallDependencies{Search: search})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpacePrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "space query", TopicLimit: intPtr(0)})
	require.NoError(t, err)
	require.Len(t, result.Results, 2)
	require.Equal(t, string(domain.MemorySpacePrivate), result.Results[0].SpaceKind)
	require.Equal(t, string(domain.MemorySpaceTeamShared), result.Results[1].SpaceKind)
	require.Equal(t, privateID.String(), search.inputs[0].SpaceID)
	require.Equal(t, sharedID.String(), search.inputs[1].SpaceID)
}

func TestRecallAcrossSpacesEmbedsQueryOnceAndReusesVector(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{contract: &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3, EmbeddingModel: "recall-model"}}
	provider := &recallEmbeddingProviderStub{}
	svc := NewRecallService(RecallDependencies{Search: search, Provider: provider})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpacePrivate},
		},
	})

	_, err := svc.Recall(ctx, RecallRequest{Query: "same vector", TopicLimit: intPtr(0)})
	require.NoError(t, err)
	require.Equal(t, 1, provider.calls)
	require.Len(t, search.inputs, 2)
	require.Equal(t, []float32{1, 2, 3}, search.inputs[0].QueryEmbedding)
	require.Equal(t, search.inputs[0].QueryEmbedding, search.inputs[1].QueryEmbedding)
}

func TestRecallAcrossSpacesReportsEmbeddingDegradationOnce(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{contract: &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3}}
	svc := NewRecallService(RecallDependencies{
		Search:   search,
		Provider: &recallProviderStub{available: false},
	})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpacePrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "degraded vector", TopicLimit: intPtr(0)})
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
		SearchStates: RecallSearchStates{Evidence: string(domain.SearchProjectionPending)},
		Results: []RecallResultItem{
			{EvidenceID: "shared-1", Rank: 2, SpaceKind: string(domain.MemorySpaceTeamShared)},
			{EvidenceID: "shared-2", Rank: 2, SpaceKind: string(domain.MemorySpaceTeamShared)},
		},
	}
	private := &RecallResult{
		SearchStates: RecallSearchStates{Evidence: string(domain.SearchProjectionFailed)},
		Results: []RecallResultItem{
			{EvidenceID: "private-1", Rank: 1, SpaceKind: string(domain.MemorySpacePrivate)},
			{EvidenceID: "shared-1", Rank: 1, SpaceKind: string(domain.MemorySpaceTeamShared)},
		},
	}

	fused := fuseRecallResults([]*RecallResult{shared, private}, 2)
	require.Len(t, fused.Results, 2)
	require.Equal(t, "shared-1", fused.Results[0].EvidenceID)
	require.Equal(t, "private-1", fused.Results[1].EvidenceID)
	require.Equal(t, string(domain.SearchProjectionFailed), fused.SearchStates.Evidence)
	require.Equal(t, string(domain.SearchProjectionFailed), fused.SearchState)
}

func TestFuseRecallResultsOrdersEqualScoresWithStrictSpaceTieBreak(t *testing.T) {
	fused := fuseRecallResults([]*RecallResult{
		{Results: []RecallResultItem{{EvidenceID: "shared", Rank: 1, SpaceKind: string(domain.MemorySpaceTeamShared)}}},
		{Results: []RecallResultItem{{EvidenceID: "profile", Rank: 1, SpaceKind: string(domain.MemorySpacePrivate)}}},
		{Results: []RecallResultItem{{EvidenceID: "credential", Rank: 1, SpaceKind: string(domain.MemorySpacePrivate)}}},
	}, 3)

	require.Equal(t, []string{"credential", "profile", "shared"}, []string{
		fused.Results[0].EvidenceID,
		fused.Results[1].EvidenceID,
		fused.Results[2].EvidenceID,
	})
}

func TestCompareRecallSpaceKindsOrdersSameRankLexically(t *testing.T) {
	require.Equal(t, -1, compareRecallSpaceKinds("credential", "profile"))
	require.Equal(t, 1, compareRecallSpaceKinds("profile", "credential"))
}

func TestFuseRecallResultsSkipsNilAndClampsLimits(t *testing.T) {
	empty := fuseRecallResults([]*RecallResult{nil}, 1)
	require.Empty(t, empty.Results)

	negative := fuseRecallResults([]*RecallResult{{}}, -1)
	require.Empty(t, negative.Results)
}

func TestRecallPrivateBranchFailureIsBounded(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	sharedID, privateID := uuid.New(), uuid.New()
	search := &spaceRecallStub{
		contract:    &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3},
		failSpaceID: privateID.String(),
	}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	svc := NewRecallService(RecallDependencies{Search: search, Metrics: metrics})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpacePrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "space query", Limit: 1, TopicLimit: intPtr(0)})
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
		contract:    &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3},
		failSpaceID: sharedID.String(),
	}
	svc := NewRecallService(RecallDependencies{Search: search})
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: teamID, IdentityID: ownerID, MembershipID: ownerID, OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedID, Kind: domain.MemorySpaceTeamShared},
			{ID: privateID, Kind: domain.MemorySpacePrivate},
		},
	})

	result, err := svc.Recall(ctx, RecallRequest{Query: "space query", TopicLimit: intPtr(0)})
	require.Error(t, err)
	require.Nil(t, result)
}

func TestFuseRecallSearchStateKeepsEmptyAndCurrentStates(t *testing.T) {
	require.Equal(t, string(domain.SearchProjectionPending), domain.CombineSearchProjectionStates("", string(domain.SearchProjectionPending)))
	require.Equal(t, string(domain.SearchProjectionCurrent), domain.CombineSearchProjectionStates(string(domain.SearchProjectionCurrent), ""))
	require.Equal(t, string(domain.SearchProjectionCurrent), domain.CombineSearchProjectionStates(string(domain.SearchProjectionNotRequired), string(domain.SearchProjectionCurrent)))
}

func TestFuseRecallResultsDeduplicatesIdenticalDegradations(t *testing.T) {
	degradation := RecallDegradationResult{
		Frontier: "evidence",
		Optional: true,
		Code:     "evidence_vector_warming",
		Message:  "Evidence vector projection is warming; lexical discovery was used.",
	}
	fused := fuseRecallResults([]*RecallResult{
		{Degradations: []RecallDegradationResult{degradation}},
		{Degradations: []RecallDegradationResult{degradation}},
	}, 1)
	require.Equal(t, []RecallDegradationResult{degradation}, fused.Degradations)
}

func intPtr(v int) *int { return &v }

type spaceRecallStub struct {
	contract       *searchcontract.ActiveSearchContract
	inputs         []recallcontract.RecallEvidenceInput
	failSpaceID    string
	lastEvidenceID string
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

func (s *spaceRecallStub) GetActiveSearchContract(context.Context) (*searchcontract.ActiveSearchContract, error) {
	return s.contract, nil
}

func (s *spaceRecallStub) ReadEvidenceCandidates(_ context.Context, input recallcontract.RecallEvidenceInput, _ *searchcontract.ActiveSearchContract, _ int) (*recallcontract.RecallCandidateBatch, error) {
	s.inputs = append(s.inputs, input)
	if s.failSpaceID != "" && input.SpaceID == s.failSpaceID {
		return nil, errors.New("space branch unavailable")
	}
	s.lastEvidenceID = uuid.NewString()
	return &recallcontract.RecallCandidateBatch{
		SearchState: string(domain.SearchProjectionCurrent),
		TextHits:    []searchcontract.SearchHit{{SourceKind: "evidence", SourceID: s.lastEvidenceID}},
	}, nil
}

func (s *spaceRecallStub) HydrateEvidence(_ context.Context, input recallcontract.RecallEvidenceInput, _ *searchcontract.ActiveSearchContract, _ []string) (map[string]recallcontract.RecallEvidenceHit, error) {
	return map[string]recallcontract.RecallEvidenceHit{
		s.lastEvidenceID: {TeamID: input.TeamID, EvidenceID: s.lastEvidenceID, Context: input.SpaceID},
	}, nil
}

func (s *spaceRecallStub) LoadRecallConflicts(context.Context, recallcontract.RecallEvidenceInput, []recallcontract.RecallEvidenceHit) (*recallcontract.RecallConflicts, error) {
	return &recallcontract.RecallConflicts{}, nil
}
