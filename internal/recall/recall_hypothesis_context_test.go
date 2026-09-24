package recall

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func TestRecallHypothesisContextFromDeduplicatesRetrievedHandles(t *testing.T) {
	result := &RecallResult{
		Results: []RecallResultItem{{
			EvidenceID:      " evidence-1 ",
			RelationshipIDs: []string{"relationship-1", "relationship-2"},
		}, {EvidenceID: "evidence-1"}},
		RelatedRelationships: []RelatedRelationshipSummary{{
			RelationshipID:            "relationship-2",
			EquivalentRelationshipIDs: []string{"relationship-3", "relationship-1"},
			EvidenceIDs:               []string{"evidence-2"},
			Subject:                   EntityHandle{EntityID: "entity-1"},
			Object:                    SemanticObject{EntityID: "entity-2"},
		}, {
			RelationshipID: "relationship-value",
			Object:         SemanticObject{ValueID: "value-1"},
		}},
		RelatedCommunities: []RecallDiscoveryPath{{
			EvidenceIDs: []string{"evidence-3"},
			TopEntities: []EntityHandle{{EntityID: "entity-3"}},
			CommunityRelationships: []RelatedRelationshipSummary{{
				RelationshipID: "relationship-4",
				Subject:        EntityHandle{EntityID: "entity-4"},
				Object:         SemanticObject{EntityID: "entity-5"},
			}},
		}},
		DiscoveryPaths: []RecallDiscoveryPath{{
			Relationships: []RecallRelationshipHandle{{
				RelationshipID: "relationship-5",
				Subject:        EntityHandle{EntityID: "entity-6"},
				Object:         SemanticObject{EntityID: "entity-7"},
			}, {
				RelationshipID: "relationship-value-path",
				Object:         SemanticObject{ValueID: "value-2"},
			}},
		}},
	}

	got := recallHypothesisContextFrom(result)
	require.Equal(t, []string{"evidence-1", "evidence-2", "evidence-3"}, got.evidenceIDs)
	require.Equal(t, []string{
		"relationship-1", "relationship-2", "relationship-3", "relationship-value", "relationship-4", "relationship-5", "relationship-value-path",
	}, got.relationshipIDs)
	require.Equal(t, []string{
		"entity-1", "entity-2", "entity-3", "entity-4", "entity-5", "entity-6", "entity-7",
	}, got.entityIDs)
	require.Equal(t, []string{"value-1", "value-2"}, got.valueIDs)
}

func TestRecallHypothesisContextFromEvidenceHitRelationships(t *testing.T) {
	result := &RecallResult{
		Results: []RecallResultItem{{
			EvidenceID:      "evidence-1",
			RelationshipIDs: []string{"relationship-1"},
		}},
	}

	got := recallHypothesisContextFrom(result)

	require.Equal(t, []string{"evidence-1"}, got.evidenceIDs)
	require.Equal(t, []string{"relationship-1"}, got.relationshipIDs)
	require.Empty(t, got.entityIDs)
	require.Empty(t, got.valueIDs)
}

func TestRecallHypothesisContextFromBoundsEachHandleKind(t *testing.T) {
	result := &RecallResult{}
	for index := 0; index <= 200; index++ {
		result.Results = append(result.Results, RecallResultItem{
			EvidenceID:      uuid.NewString(),
			RelationshipIDs: []string{uuid.NewString()},
		})
		result.RelatedRelationships = append(result.RelatedRelationships, RelatedRelationshipSummary{
			Subject: EntityHandle{EntityID: fmt.Sprintf("entity-%03d", index)},
			Object:  SemanticObject{ValueID: fmt.Sprintf("value-%03d", index)},
		})
	}

	got := recallHypothesisContextFrom(result)
	require.Len(t, got.evidenceIDs, 200)
	require.Len(t, got.relationshipIDs, 200)
	require.Len(t, got.entityIDs, 200)
	require.Len(t, got.valueIDs, 200)
	require.Equal(t, "entity-000", got.entityIDs[0])
	require.Equal(t, "entity-199", got.entityIDs[199])
	require.Equal(t, "value-000", got.valueIDs[0])
	require.Equal(t, "value-199", got.valueIDs[199])
}

func TestRecallPassesValueEndpointsToHypothesisReader(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	valueID := uuid.NewString()
	search := &recallSearchStub{
		contract: &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3},
		result: &recallcontract.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results:     []recallcontract.RecallEvidenceHit{},
		},
		relationshipResult: &recallcontract.RecallRelationshipsResult{
			TeamID:      teamID.String(),
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []recallcontract.RecallRelationshipHit{{
				RelationshipID:  uuid.NewString(),
				ObjectValueID:   valueID,
				ObjectValueType: "string",
			}},
		},
	}
	hypotheses := &recallHypothesisStub{}
	svc := NewRecallService(RecallDependencies{Search: search, Hypotheses: hypotheses})

	_, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{IncludeHypotheses: true})
	require.NoError(t, err)
	require.Equal(t, []string{valueID}, hypotheses.recallInput.ValueIDs)
}

func TestRecallPassesEvidenceHitRelationshipsToHypothesisReader(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	hypothesisID := uuid.NewString()
	search := &recallSearchStub{
		contract: &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3},
		result: &recallcontract.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []recallcontract.RecallEvidenceHit{{
				EvidenceID:      evidenceID,
				RelationshipIDs: []string{relationshipID},
				Rank:            1,
			}},
		},
	}
	hypotheses := &recallHypothesisStub{records: []dreamcontract.HypothesisRecord{{
		HypothesisID: hypothesisID,
		SourceRefs:   []map[string]any{{"type": "relationship", "id": relationshipID}},
	}}}
	svc := NewRecallService(RecallDependencies{Search: search, Hypotheses: hypotheses})

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{IncludeHypotheses: true})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Empty(t, result.RelatedRelationships)
	require.Empty(t, result.RelatedCommunities)
	require.Empty(t, hypotheses.recallInput.Query)
	require.Equal(t, []string{evidenceID}, hypotheses.recallInput.EvidenceIDs)
	require.Equal(t, []string{relationshipID}, hypotheses.recallInput.RelationshipIDs)
	require.Len(t, result.RelatedHypotheses, 1)
	require.Equal(t, hypothesisID, result.RelatedHypotheses[0].HypothesisID)
	require.Empty(t, result.RelatedHypotheses[0].SourceEvidenceIDs)
	require.Equal(t, []string{relationshipID}, result.RelatedHypotheses[0].SourceRelationshipIDs)
}

func TestRecallOmitsRelatedHypothesesForKnownAt(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	evidenceID := uuid.NewString()
	search := &recallSearchStub{
		contract: &searchcontract.ActiveSearchContract{EmbeddingDimensions: 3},
		result: &recallcontract.RecallEvidenceResult{
			SearchState: string(domain.SearchProjectionCurrent),
			Results: []recallcontract.RecallEvidenceHit{{
				EvidenceID: evidenceID,
				Rank:       1,
			}},
		},
	}
	hypotheses := &recallHypothesisStub{records: []dreamcontract.HypothesisRecord{{
		HypothesisID: uuid.NewString(),
		Statement:    "A hypothesis created after the requested snapshot.",
	}}}
	svc := NewRecallService(RecallDependencies{Search: search, Hypotheses: hypotheses})
	knownAt := time.Now().UTC().Add(-time.Hour)

	result, err := svc.Recall(authenticatedRememberContext(teamID, profileID, keyID), RecallRequest{
		IncludeHypotheses: true,
		KnownAt:           &knownAt,
	})
	require.NoError(t, err)
	require.Empty(t, result.RelatedHypotheses)
	require.Len(t, result.Degradations, 1)
	require.Equal(t, "related_hypotheses_temporal_not_supported", result.Degradations[0].Code)
	require.True(t, result.Degradations[0].Optional)
	require.Empty(t, hypotheses.recallInput.TeamID)
}
