package recall

import (
	"fmt"
	"testing"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/stretchr/testify/require"
)

func TestRecallOntologySelectionPreservesRanksFactsAndKnownGroups(t *testing.T) {
	hydrated := &recallcontract.EvidenceHydration{Hits: map[string]recallcontract.RecallEvidenceHit{
		"E1": {EvidenceID: "E1", Context: "Atlas uses PostgreSQL.", RelationshipIDs: []string{"R1"}},
		"E2": {EvidenceID: "E2", Context: "PostgreSQL is Atlas's datastore.", RelationshipIDs: []string{"R2"}},
		"E3": {EvidenceID: "E3", Context: "Atlas uses PostgreSQL and Redis."},
	}, Groups: []ontology.RecallEvidenceGroup{{ID: "G1", Members: []string{"E1", "E2", "ineligible"}}}}
	candidates := []recallCandidate{{ID: "E2", Score: 0.9}, {ID: "E1", Score: 0.8}, {ID: "E3", Score: 0.7}}
	results, sources, code := selectRecallEvidence(candidates, hydrated, recallcontract.RecallEvidenceInput{Limit: 2, SpaceKind: "team_shared"})
	require.Empty(t, code)
	require.Len(t, results, 2)
	require.Equal(t, "E2", results[0].EvidenceID)
	require.Equal(t, 0.9, results[0].Score)
	require.Equal(t, []string{"E1"}, results[0].EquivalentEvidenceIDs)
	require.ElementsMatch(t, []string{"R1", "R2"}, results[0].RelationshipIDs)
	require.Equal(t, "E3", results[1].EvidenceID)
	require.Equal(t, 2, results[1].Rank)
	require.Len(t, sources, 3)
	results, _, _ = selectRecallEvidence(candidates, hydrated, recallcontract.RecallEvidenceInput{Limit: 2, KnownEvidenceIDs: []string{"E1"}})
	require.Len(t, results, 1)
	require.Equal(t, "E3", results[0].EvidenceID)
	results, _, _ = selectRecallEvidence(candidates, hydrated, recallcontract.RecallEvidenceInput{Limit: 2, KnownEvidenceIDs: []string{"ineligible"}})
	require.Len(t, results, 2)
}

func TestRecallOntologySelectionRejectsOverlappingGroups(t *testing.T) {
	hydrated := &recallcontract.EvidenceHydration{Hits: map[string]recallcontract.RecallEvidenceHit{}, Groups: []ontology.RecallEvidenceGroup{
		{ID: "G1", Members: []string{"E1", "E2"}}, {ID: "G2", Members: []string{"E2", "E3"}},
	}}
	candidates := []recallCandidate{}
	for _, id := range []string{"E1", "E2", "E3"} {
		hydrated.Hits[id] = recallcontract.RecallEvidenceHit{EvidenceID: id}
		candidates = append(candidates, recallCandidate{ID: id})
	}
	results, _, code := selectRecallEvidence(candidates, hydrated, recallcontract.RecallEvidenceInput{Limit: 3})
	require.Len(t, results, 3)
	require.Equal(t, "ontology_group_overlap", code)
	for _, result := range results {
		require.Empty(t, result.EquivalentEvidenceIDs)
	}
}

func TestRecallOntologyEquivalentEvidenceBoundAndProvenance(t *testing.T) {
	hydrated := &recallcontract.EvidenceHydration{Hits: map[string]recallcontract.RecallEvidenceHit{}}
	group := ontology.RecallEvidenceGroup{ID: "G1"}
	for i := range 24 {
		id := fmt.Sprintf("E%02d", i)
		group.Members = append(group.Members, id)
		hydrated.Hits[id] = recallcontract.RecallEvidenceHit{EvidenceID: id}
	}
	hydrated.Groups = []ontology.RecallEvidenceGroup{group}
	results, _, _ := selectRecallEvidence([]recallCandidate{{ID: "E00"}}, hydrated, recallcontract.RecallEvidenceInput{Limit: 1})
	require.Len(t, results[0].EquivalentEvidenceIDs, 20)
	require.True(t, results[0].EquivalentsTruncated)
	require.NotContains(t, results[0].EquivalentEvidenceIDs, "E00")
	result := &RecallResult{Results: []RecallResultItem{{EvidenceID: "E00", EquivalentEvidenceIDs: results[0].EquivalentEvidenceIDs, Rank: 1}}}
	require.Len(t, recallResultEvidenceIDs(result.Results), 21)
	require.Len(t, recallHypothesisContextFrom(result).evidenceIDs, 21)
	refs := FeedbackResultRefs(result)
	require.Len(t, refs, 21)
	for _, ref := range refs {
		require.Equal(t, 1, ref.Rank)
	}
}
