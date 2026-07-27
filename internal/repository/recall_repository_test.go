package repository

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallBranchFusionSkipsKnownEvidenceAndCarriesPendingState(t *testing.T) {
	knownID := uuid.NewString()
	evidenceA := uuid.NewString()
	evidenceB := uuid.NewString()
	acc := map[string]*recallCandidate{}
	known := recallStringSet([]string{knownID})

	addRecallBranch(acc, []SearchHit{
		{SourceKind: "evidence", SourceID: evidenceA, SearchState: string(domain.SearchProjectionCurrent)},
		{SourceKind: "evidence", SourceID: evidenceB, SearchState: string(domain.SearchProjectionPending)},
		{SourceKind: "evidence", SourceID: knownID, SearchState: string(domain.SearchProjectionCurrent)},
		{SourceKind: "relationship", SourceID: uuid.NewString(), SearchState: string(domain.SearchProjectionCurrent)},
	}, known, 1)
	addRecallBranch(acc, []SearchHit{
		{SourceKind: "evidence", SourceID: evidenceB, SearchState: string(domain.SearchProjectionCurrent)},
	}, known, 1)

	ranked := sortedRecallCandidates(acc)
	if len(ranked) != 2 {
		t.Fatalf("ranked candidates = %#v, want two unknown evidence candidates", ranked)
	}
	if ranked[0].EvidenceID != evidenceB {
		t.Fatalf("top candidate = %s, want fused evidence %s", ranked[0].EvidenceID, evidenceB)
	}
	if ranked[0].SearchState != string(domain.SearchProjectionPending) {
		t.Fatalf("search state = %q, want pending", ranked[0].SearchState)
	}
	if _, ok := acc[knownID]; ok {
		t.Fatal("known evidence was added to recall candidates")
	}
}

func TestRecallCommunityBranchDoesNotOutrankPrimaryCandidates(t *testing.T) {
	acc := map[string]*recallCandidate{}
	known := map[string]struct{}{}
	primary := make([]SearchHit, 0, 250)
	for i := 0; i < 250; i++ {
		primary = append(primary, SearchHit{
			SourceKind:  "evidence",
			SourceID:    uuid.NewString(),
			SearchState: string(domain.SearchProjectionCurrent),
		})
	}
	communityEvidenceID := uuid.NewString()

	addRecallBranch(acc, primary, known, 0.5)
	addRecallBranch(acc, []SearchHit{{
		SourceKind:  "evidence",
		SourceID:    communityEvidenceID,
		SearchState: string(domain.SearchProjectionCurrent),
	}}, known, 0.05)

	ranked := sortedRecallCandidates(acc)
	require.Len(t, ranked, 251)
	require.Equal(t, communityEvidenceID, ranked[len(ranked)-1].EvidenceID)
}

func TestRecallInputNormalizationAndValidation(t *testing.T) {
	teamID := uuid.NewString()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	entityID := uuid.NewString()
	input := normalizeRecallEvidenceInput(RecallEvidenceInput{
		TeamID:               " " + teamID + " ",
		Query:                " durable memory ",
		Limit:                500,
		KnownEvidenceIDs:     []string{" " + evidenceID + " ", evidenceID, ""},
		KnownRelationshipIDs: []string{relationshipID},
		ExpandFromEntityIDs:  []string{entityID, entityID},
	})

	if input.TeamID != teamID || input.Query != "durable memory" || input.Limit != maxRecallLimit {
		t.Fatalf("normalized input = %#v", input)
	}
	if len(input.KnownEvidenceIDs) != 1 || input.KnownEvidenceIDs[0] != evidenceID {
		t.Fatalf("known evidence = %#v", input.KnownEvidenceIDs)
	}
	if len(input.ExpandFromEntityIDs) != 1 || input.ExpandFromEntityIDs[0] != entityID {
		t.Fatalf("expand entities = %#v", input.ExpandFromEntityIDs)
	}
	if err := validateRecallEvidenceInput(input); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	missingQuery := input
	missingQuery.Query = ""
	missingQuery.ExpandFromEntityIDs = nil
	if err := validateRecallEvidenceInput(missingQuery); err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("missing query err = %v, want query validation", err)
	}

	badID := input
	badID.KnownRelationshipIDs = []string{"not-a-uuid"}
	if err := validateRecallEvidenceInput(badID); err == nil || !strings.Contains(err.Error(), "known_relationship_ids") {
		t.Fatalf("bad relationship id err = %v, want relationship id validation", err)
	}
}

func TestRecallBoundsAndContextHelpers(t *testing.T) {
	if got := recallOverfetchLimit(1); got != recallOverfetchFloor {
		t.Fatalf("overfetch floor = %d, want %d", got, recallOverfetchFloor)
	}
	if got := recallOverfetchLimit(100); got != recallOverfetchCap {
		t.Fatalf("overfetch cap = %d, want %d", got, recallOverfetchCap)
	}
	if got := recallCombinedSearchState("", string(domain.SearchProjectionCurrent)); got != string(domain.SearchProjectionCurrent) {
		t.Fatalf("combined state = %q", got)
	}
	if got := recallCombinedSearchState(string(domain.SearchProjectionCurrent), string(domain.SearchProjectionPending)); got != string(domain.SearchProjectionPending) {
		t.Fatalf("combined pending state = %q", got)
	}
	long := strings.Repeat("a", 2100)
	if got := truncateRecallContext(long); len(got) != 2000 {
		t.Fatalf("truncated length = %d, want 2000", len(got))
	}
}

func TestRecallANNHelpersUseDerivedContract(t *testing.T) {
	contractID := uuid.NewString()
	contract := &ActiveSearchContract{
		EmbeddingContractID: contractID,
		EmbeddingDimensions: 3072,
		IndexStrategy:       string(domain.VectorIndexHalfvecHNSW),
		CandidateLimit:      120,
	}
	expression, err := recallANNDistanceExpression(contract)
	require.NoError(t, err)
	require.Equal(t, "embedding::halfvec(3072) <=> ?::halfvec(3072)", expression)

	literal, err := recallEmbeddingContractLiteral(contractID)
	require.NoError(t, err)
	require.Equal(t, "'"+contractID+"'", literal)
	require.Equal(t, 120, recallANNCandidateLimit(contract, 60))
	require.Equal(t, 80, recallANNCandidateLimit(&ActiveSearchContract{CandidateLimit: 20}, 80))
	require.Equal(t, recallOverfetchCap, recallANNCandidateLimit(&ActiveSearchContract{CandidateLimit: 1000}, 80))

	_, err = recallANNDistanceExpression(&ActiveSearchContract{
		EmbeddingDimensions: 5000,
		IndexStrategy:       string(domain.VectorIndexHalfvecHNSW),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSearchContractMismatch), "err=%v", err)
}
