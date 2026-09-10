package recall

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	communitycontract "github.com/markhuangai/dense-mem/internal/community/contract"
	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func intPointer(value int) *int {
	return &value
}

func TestRecallUsesCurrentCommunitySnapshotAndCoverage(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	communityLimit := 2
	communityRelationshipLimit := 1
	search := &recallSearchStub{
		contract: &searchcontract.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &recallcontract.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results:     []recallcontract.RecallEvidenceHit{},
		},
		relationshipResult: &recallcontract.RecallRelationshipsResult{
			TeamID:      teamID.String(),
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []recallcontract.RecallRelationshipHit{{
				RelationshipID:   uuid.NewString(),
				SemanticGroupKey: "direct-group",
				SubjectEntityID:  uuid.NewString(),
				SubjectName:      "Dense-Mem",
				PredicateKey:     "uses",
				ObjectEntityID:   uuid.NewString(),
				ObjectName:       "PostgreSQL",
				Polarity:         "+",
			}},
		},
	}
	communityID := uuid.NewString()
	relationshipID := uuid.NewString()
	communities := &recallCommunitySnapshotStub{
		covered: []string{"covered-group"},
		records: []communitycontract.CommunityRecallRecord{{
			CommunityID:        communityID,
			LogicalCommunityID: uuid.NewString(),
			Rank:               1,
			Summary:            "Durable memory uses PostgreSQL.",
			TopEntities:        []communitycontract.CommunityRecallTopEntity{{EntityID: uuid.NewString(), Name: "Dense-Mem"}},
			TopPredicates:      []string{"uses"},
			EntityCount:        2,
			RelationshipCount:  1,
			Relationships: []recallcontract.RecallRelationshipHit{{
				RelationshipID:   relationshipID,
				SemanticGroupKey: "community-group",
				SubjectEntityID:  uuid.NewString(),
				SubjectName:      "Dense-Mem",
				PredicateKey:     "uses",
				ObjectEntityID:   uuid.NewString(),
				ObjectName:       "PostgreSQL",
				Polarity:         "+",
				EvidenceIDs:      []string{uuid.NewString()},
			}},
		}},
	}
	svc := NewRecallService(RecallDependencies{
		Search: search, Communities: communities,
		CommunityConfig: recallCommunityConfigStub{enabled: true},
	})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		Limit:                      3,
		RelationshipLimit:          intPointer(1),
		CommunityLimit:             &communityLimit,
		CommunityRelationshipLimit: &communityRelationshipLimit,
	})
	require.NoError(t, err)
	require.Len(t, result.RelatedCommunities, 1)
	require.Equal(t, communityID, result.RelatedCommunities[0].CommunityID)
	require.Equal(t, relationshipID, result.RelatedCommunities[0].CommunityRelationships[0].RelationshipID)
	require.NotNil(t, result.RelatedCommunities[0].CommunityRelationships[0].EquivalentRelationshipIDs)
	require.Empty(t, result.RelatedCommunities[0].CommunityRelationships[0].EquivalentRelationshipIDs)
	require.Empty(t, result.DiscoveryPaths)
	require.Equal(t, []string{"covered-group", "direct-group"}, communities.snapshotInput.ExcludedGroupKeys)
	require.Equal(t, communities.snapshotInput.ExcludedGroupKeys, communities.snapshotInput.CoveredGroupKeys)
	require.Equal(t, 1, communities.snapshotInput.RelationshipLimit)
	require.Equal(t, 1, search.relationshipCalls)
	require.Equal(t, []string{"covered-group"}, search.relationshipInput.ExcludedGroupKeys)
	require.Empty(t, result.RelatedRelationships)

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"related_communities"`)
	require.Contains(t, string(encoded), `"relationships"`)
	require.Contains(t, string(encoded), `"equivalent_relationship_ids":[]`)
}

func TestRecallCommunitiesReportsTemporalDegradation(t *testing.T) {
	limit := 1
	validAt := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	communities := &recallCommunitySnapshotStub{}
	svc := &recallService{
		communities:     communities,
		communityConfig: recallCommunityConfigStub{enabled: true},
	}

	records, paths, degradation := svc.recallCommunities(context.Background(), uuid.NewString(), RecallRequest{
		CommunityLimit: &limit,
		ValidAt:        &validAt,
	}, map[string]struct{}{}, nil, nil, true)
	require.Empty(t, records)
	require.Empty(t, paths)
	require.Equal(t, "community_temporal_not_supported", degradation.Code)
	require.True(t, degradation.Optional)
	require.Empty(t, communities.recallInput.TeamID)
}

func TestRecallCommunitiesReportsTerminalRunStatuses(t *testing.T) {
	limit := 1
	for _, status := range []string{"failed", "cancelled", "too_large", "running"} {
		communities := &recallCommunityRunStub{status: status}
		svc := &recallService{
			communities:     communities,
			communityConfig: recallCommunityConfigStub{enabled: true},
		}

		records, paths, degradation := svc.recallCommunities(context.Background(), uuid.NewString(), RecallRequest{
			CommunityLimit: &limit,
		}, map[string]struct{}{}, nil, nil, true)
		require.Empty(t, records)
		require.Empty(t, paths)
		require.NotNil(t, degradation)
		require.True(t, degradation.Optional)
	}
}

func TestRecallCommunitiesRejectsIncompatibleCompletedSnapshot(t *testing.T) {
	limit := 1
	communities := &recallCommunityRunStub{
		status:            "completed",
		algorithmKind:     "connected_components",
		algorithmVersion:  "v1",
		profileVersion:    communitycontract.CommunityProfileVersion,
		configurationHash: "sha256:legacy",
	}
	svc := &recallService{
		communities:     communities,
		communityConfig: recallCommunityConfigStub{enabled: true},
	}

	records, paths, degradation := svc.recallCommunities(context.Background(), uuid.NewString(), RecallRequest{
		CommunityLimit: &limit,
	}, map[string]struct{}{}, nil, nil, true)

	require.Empty(t, records)
	require.Empty(t, paths)
	require.Equal(t, "community_snapshot_unavailable", degradation.Code)
}

func TestRecallDiscoveryPathMarshalLegacyShape(t *testing.T) {
	encoded, err := json.Marshal(RecallDiscoveryPath{
		Relationships: []RecallRelationshipHandle{{RelationshipID: "relationship-1"}},
		EvidenceIDs:   []string{"evidence-1"},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"relationships":[{"relationship_id":"relationship-1","subject":{"entity_id":"","name":""},"predicate":"","object":{},"polarity":""}],"evidence_ids":["evidence-1"]}`, string(encoded))
}

func TestSortedGroupKeys(t *testing.T) {
	require.Equal(t, []string{"g-1", "g-2"}, sortedGroupKeys(map[string]struct{}{"g-2": {}, " ": {}, "g-1": {}}))
}

type recallCommunitySnapshotStub struct {
	recallCommunityStub
	covered       []string
	coverageErr   error
	records       []communitycontract.CommunityRecallRecord
	recallErr     error
	snapshotInput communitycontract.CommunityRecallInput
}

type recallCommunityRunStub struct {
	recallCommunitySnapshotStub
	status            string
	algorithmKind     string
	algorithmVersion  string
	profileVersion    string
	configurationHash string
}

func (s *recallCommunityRunStub) LatestCommunityRun(_ context.Context, _ string) (*communitycontract.CommunityRun, error) {
	return &communitycontract.CommunityRun{
		Status: s.status, AlgorithmKind: s.algorithmKind, AlgorithmVersion: s.algorithmVersion,
		ProfileVersion: s.profileVersion, ConfigurationHash: s.configurationHash,
	}, nil
}

func (s *recallCommunitySnapshotStub) ListCommunitySemanticGroups(_ context.Context, _ communitycontract.CommunityCoverageInput) ([]string, error) {
	if s.coverageErr != nil {
		return nil, s.coverageErr
	}
	return append([]string(nil), s.covered...), nil
}

func (s *recallCommunitySnapshotStub) RecallCommunities(_ context.Context, input communitycontract.CommunityRecallInput) ([]communitycontract.CommunityRecallRecord, error) {
	s.recallInput = communitycontract.CommunityDiscoveryInput{TeamID: input.TeamID, Query: input.Query, Limit: input.Limit}
	// Keep the complete input separately so tests can inspect suppression keys.
	s.snapshotInput = input
	if s.recallErr != nil {
		return nil, s.recallErr
	}
	return s.records, nil
}

var _ RecallCommunityRepository = (*recallCommunitySnapshotStub)(nil)
var _ RecallCommunitySnapshotRepository = (*recallCommunitySnapshotStub)(nil)
var _ RecallCommunityCoverageRepository = (*recallCommunitySnapshotStub)(nil)
