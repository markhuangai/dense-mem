package memoryservice

import (
	"encoding/json"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"

	"github.com/stretchr/testify/require"
)

func TestRecallCommunityMarshalUsesExactPublicShape(t *testing.T) {
	encoded, err := json.Marshal(map[string]any{
		"related_communities": []RecallDiscoveryPath{{
			CommunityID:        "community-1",
			LogicalCommunityID: "logical-community-1",
			Rank:               1,
			Summary:            "A bounded summary",
			TopEntities:        []EntityHandle{{EntityID: "entity-1", Name: "Dense-Mem"}},
			TopPredicates:      []string{"uses"},
			EntityCount:        2,
			RelationshipCount:  1,
			CommunityRelationships: []RelatedRelationshipSummary{{
				RelationshipID: "relationship-1",
				Subject:        EntityHandle{EntityID: "entity-1", Name: "Dense-Mem"},
				Predicate:      "uses",
				Object:         SemanticObject{EntityID: "entity-2", Name: "PostgreSQL"},
				Polarity:       "+",
				EvidenceIDs:    []string{"evidence-1"},
				SpaceKind:      string(domain.MemorySpaceTeamShared),
			}},
			RelationshipsTruncated: false,
		}},
	})
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(encoded, &body))
	community := body["related_communities"].([]any)[0].(map[string]any)
	require.Contains(t, community, "relationships")
	require.Equal(t, "logical-community-1", community["logical_community_id"])
	require.NotContains(t, community, "evidence_ids")
	require.NotContains(t, community, "related_relationships")
	require.Equal(t, "relationship-1", community["relationships"].([]any)[0].(map[string]any)["relationship_id"])
}
