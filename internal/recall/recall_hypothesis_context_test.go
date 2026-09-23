package recall

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
			}},
		}},
	}

	got := recallHypothesisContextFrom(result)
	require.Equal(t, []string{"evidence-1", "evidence-2", "evidence-3"}, got.evidenceIDs)
	require.Equal(t, []string{
		"relationship-1", "relationship-2", "relationship-3", "relationship-4", "relationship-5",
	}, got.relationshipIDs)
	require.Equal(t, []string{
		"entity-1", "entity-2", "entity-3", "entity-4", "entity-5", "entity-6", "entity-7",
	}, got.entityIDs)
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
		})
	}

	got := recallHypothesisContextFrom(result)
	require.Len(t, got.evidenceIDs, 200)
	require.Len(t, got.relationshipIDs, 200)
	require.Len(t, got.entityIDs, 200)
	require.Equal(t, "entity-000", got.entityIDs[0])
	require.Equal(t, "entity-199", got.entityIDs[199])
}
