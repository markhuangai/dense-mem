package memoryservice

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestRecallUsesCurrentCommunitySnapshotAndCoverage(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	communityLimit := 2
	communityRelationshipLimit := 1
	search := &recallSearchStub{
		contract: &repository.ActiveSearchContract{
			EmbeddingContractID: uuid.NewString(),
			EmbeddingDimensions: 3,
			EmbeddingModel:      "test-model",
		},
		result: &repository.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results:     []repository.RecallEvidenceHit{},
		},
		relationshipResult: &repository.RecallRelationshipsResult{
			TeamID:      teamID.String(),
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []repository.RecallRelationshipHit{{
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
		records: []repository.CommunityRecallRecord{{
			CommunityID:       communityID,
			Rank:              1,
			Summary:           "Durable memory uses PostgreSQL.",
			TopEntities:       []repository.CommunityRecallTopEntity{{EntityID: uuid.NewString(), Name: "Dense-Mem"}},
			TopPredicates:     []string{"uses"},
			EntityCount:       2,
			RelationshipCount: 1,
			Relationships: []repository.RecallRelationshipHit{{
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
		ContractVersion:            domain.ContractVersion,
		Limit:                      3,
		RelationshipLimit:          intPointer(1),
		CommunityLimit:             &communityLimit,
		CommunityRelationshipLimit: &communityRelationshipLimit,
	})
	require.NoError(t, err)
	require.Len(t, result.RelatedCommunities, 1)
	require.Equal(t, communityID, result.RelatedCommunities[0].CommunityID)
	require.Equal(t, relationshipID, result.RelatedCommunities[0].CommunityRelationships[0].RelationshipID)
	require.Empty(t, result.DiscoveryPaths)
	require.Equal(t, []string{"covered-group", "direct-group"}, communities.snapshotInput.ExcludedGroupKeys)
	require.Equal(t, communities.snapshotInput.ExcludedGroupKeys, communities.snapshotInput.CoveredGroupKeys)
	require.Equal(t, 1, communities.snapshotInput.RelationshipLimit)
	require.Equal(t, []string{"community-group", "covered-group", "direct-group"}, search.relationshipInput.ExcludedGroupKeys)

	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"related_communities"`)
	require.Contains(t, string(encoded), `"relationships"`)
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
	records       []repository.CommunityRecallRecord
	recallErr     error
	snapshotInput repository.CommunityRecallInput
}

type recallCommunityRunStub struct {
	recallCommunitySnapshotStub
	status string
}

func (s *recallCommunityRunStub) LatestCommunityRun(_ context.Context, _ string) (*repository.CommunityRun, error) {
	return &repository.CommunityRun{Status: s.status}, nil
}

func (s *recallCommunitySnapshotStub) ListCommunitySemanticGroups(_ context.Context, _ repository.CommunityCoverageInput) ([]string, error) {
	if s.coverageErr != nil {
		return nil, s.coverageErr
	}
	return append([]string(nil), s.covered...), nil
}

func (s *recallCommunitySnapshotStub) RecallCommunities(_ context.Context, input repository.CommunityRecallInput) ([]repository.CommunityRecallRecord, error) {
	s.recallInput = repository.CommunityDiscoveryInput{TeamID: input.TeamID, Query: input.Query, Limit: input.Limit}
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
