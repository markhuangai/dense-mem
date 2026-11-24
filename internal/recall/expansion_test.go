package recall

import (
	"fmt"
	"testing"

	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/stretchr/testify/require"
)

func TestBridgeRankingPrefersIndependentSeedsAndReservesTwoPlaces(t *testing.T) {
	paths := []recallcontract.EvidenceBridge{
		{SeedID: "1", EvidenceID: "peer-a", Term: "zirconium", Fanout: 3},
		{SeedID: "1", EvidenceID: "peer-b", TopicID: "topic", Fanout: 2},
		{SeedID: "1", EvidenceID: "peer-a", TopicID: "topic", Fanout: 3},
		{SeedID: "2", EvidenceID: "peer-a", TopicID: "other", Fanout: 2},
		{SeedID: "foreign", EvidenceID: "forbidden", Term: "other", Fanout: 1},
	}
	hits, bridges := rankEvidenceBridges([]string{"1", "2"}, paths)
	require.Len(t, hits, 2)
	require.Equal(t, "peer-a", hits[0].SourceID)
	require.InDelta(t, 1.0/3+1.0/4, hits[0].TextRank, 1e-10)
	candidates := []recallCandidate{}
	hydrated := &recallcontract.EvidenceHydration{Hits: map[string]recallcontract.RecallEvidenceHit{}}
	for i := 1; i <= 55; i++ {
		id := fmt.Sprint(i)
		hydrated.Hits[id] = recallcontract.RecallEvidenceHit{EvidenceID: id, Context: "source"}
		candidates = append(candidates, recallCandidate{ID: id})
	}
	for _, hit := range hits {
		hydrated.Hits[hit.SourceID] = recallcontract.RecallEvidenceHit{EvidenceID: hit.SourceID}
		candidates = append(candidates, recallCandidate{ID: hit.SourceID})
	}
	ranked := reserveBridgeResults(candidates, bridges, hydrated, nil)
	require.Equal(t, "peer-a", ranked[4].ID)
	require.Equal(t, "peer-b", ranked[9].ID)
	require.Equal(t, "1", ranked[0].ID)
	require.Equal(t, "source", hydrated.Hits["peer-a"].Bridge.SeedExcerpt)
}
