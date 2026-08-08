package community

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectIsDeterministicAndUsesSharedEvidenceAndEntities(t *testing.T) {
	inputs := []Input{
		{RelationshipID: "r3", SemanticGroupKey: "g3", SubjectEntityID: "e2", ObjectEntityID: "e3", EvidenceIDs: []string{"ev-shared"}},
		{RelationshipID: "r1", SemanticGroupKey: "g1", SubjectEntityID: "e1", ObjectEntityID: "e2", EvidenceIDs: []string{"ev-shared"}},
		{RelationshipID: "r2", SemanticGroupKey: "g2", SubjectEntityID: "e1", ObjectEntityID: "e4", EvidenceIDs: []string{"ev-other"}},
	}
	first := Detect(inputs, 158)
	second := Detect(inputs, 158)
	require.Equal(t, first.Nodes, second.Nodes)
	require.Equal(t, first.Edges, second.Edges)
	require.Equal(t, first.Clusters, second.Clusters)
	require.Len(t, first.Nodes, 3)
	require.NotEmpty(t, first.Edges)
	require.NotEmpty(t, first.Clusters)
	require.Equal(t, first.Clusters[0].CommunityID, stableCommunityID(first.Clusters[0].GroupKeys))
}

func TestDetectRejectsBoundExceededGraphWithoutPartialClusters(t *testing.T) {
	inputs := make([]Input, MaxNodes+1)
	for i := range inputs {
		inputs[i] = Input{RelationshipID: "r", SemanticGroupKey: fmt.Sprintf("g-%d", i)}
	}
	result := Detect(inputs, 1)
	require.True(t, result.TooLarge)
	require.Empty(t, result.Clusters)
}

func TestConfigurationHashAndGroupIndex(t *testing.T) {
	result := Detect([]Input{
		{RelationshipID: "r-2", SemanticGroupKey: "g-2", SubjectEntityID: "e-2", ObjectEntityID: "e-1", EvidenceIDs: []string{"ev-2"}},
		{RelationshipID: "r-1", SemanticGroupKey: "g-1", SubjectEntityID: "e-1", ObjectEntityID: "e-3", EvidenceIDs: []string{"ev-1"}},
	}, 158)
	require.Equal(t, map[string]int{"g-1": 0, "g-2": 1}, GroupIndex(result))
	require.NotEqual(t, ConfigurationHash(158), ConfigurationHash(159))
	require.Contains(t, ConfigurationHash(158), "sha256:")
}

func TestDetectNormalizesAndDeduplicatesInputs(t *testing.T) {
	result := Detect([]Input{
		{RelationshipID: "ignored", SemanticGroupKey: " "},
		{RelationshipID: " r ", SemanticGroupKey: " g ", SubjectEntityID: "e", ObjectEntityID: "e2", ObjectValueID: "value", EvidenceIDs: []string{" ev ", "ev", ""}},
		{RelationshipID: "r", SemanticGroupKey: "g", SubjectEntityID: "e", ObjectEntityID: "e2", EvidenceIDs: []string{"ev"}},
	}, 0)
	require.Len(t, result.Nodes, 1)
	require.Equal(t, []string{"r"}, result.Nodes[0].RelationshipIDs)
	require.Equal(t, []string{"e", "e2"}, result.Nodes[0].EntityIDs)
	require.Equal(t, []string{"ev"}, result.Nodes[0].EvidenceIDs)
	require.Empty(t, result.Edges)
}
