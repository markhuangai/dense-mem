package repository

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestV2RecallBranchFusionSkipsKnownEvidenceAndCarriesPendingState(t *testing.T) {
	knownID := uuid.NewString()
	evidenceA := uuid.NewString()
	evidenceB := uuid.NewString()
	acc := map[string]*v2RecallCandidate{}
	known := v2RecallStringSet([]string{knownID})

	addV2RecallBranch(acc, []V2SearchHit{
		{SourceKind: "evidence", SourceID: evidenceA, SearchState: string(domain.V2SearchProjectionCurrent)},
		{SourceKind: "evidence", SourceID: evidenceB, SearchState: string(domain.V2SearchProjectionPending)},
		{SourceKind: "evidence", SourceID: knownID, SearchState: string(domain.V2SearchProjectionCurrent)},
		{SourceKind: "relationship", SourceID: uuid.NewString(), SearchState: string(domain.V2SearchProjectionCurrent)},
	}, known, 1)
	addV2RecallBranch(acc, []V2SearchHit{
		{SourceKind: "evidence", SourceID: evidenceB, SearchState: string(domain.V2SearchProjectionCurrent)},
	}, known, 1)

	ranked := sortedV2RecallCandidates(acc)
	if len(ranked) != 2 {
		t.Fatalf("ranked candidates = %#v, want two unknown evidence candidates", ranked)
	}
	if ranked[0].EvidenceID != evidenceB {
		t.Fatalf("top candidate = %s, want fused evidence %s", ranked[0].EvidenceID, evidenceB)
	}
	if ranked[0].SearchState != string(domain.V2SearchProjectionPending) {
		t.Fatalf("search state = %q, want pending", ranked[0].SearchState)
	}
	if _, ok := acc[knownID]; ok {
		t.Fatal("known evidence was added to recall candidates")
	}
}

func TestV2RecallCommunityBranchDoesNotOutrankPrimaryCandidates(t *testing.T) {
	acc := map[string]*v2RecallCandidate{}
	known := map[string]struct{}{}
	primary := make([]V2SearchHit, 0, 250)
	for i := 0; i < 250; i++ {
		primary = append(primary, V2SearchHit{
			SourceKind:  "evidence",
			SourceID:    uuid.NewString(),
			SearchState: string(domain.V2SearchProjectionCurrent),
		})
	}
	communityEvidenceID := uuid.NewString()

	addV2RecallBranch(acc, primary, known, 0.5)
	addV2RecallBranch(acc, []V2SearchHit{{
		SourceKind:  "evidence",
		SourceID:    communityEvidenceID,
		SearchState: string(domain.V2SearchProjectionCurrent),
	}}, known, 0.05)

	ranked := sortedV2RecallCandidates(acc)
	require.Len(t, ranked, 251)
	require.Equal(t, communityEvidenceID, ranked[len(ranked)-1].EvidenceID)
}

func TestV2RecallInputNormalizationAndValidation(t *testing.T) {
	teamID := uuid.NewString()
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	entityID := uuid.NewString()
	input := normalizeV2RecallEvidenceInput(V2RecallEvidenceInput{
		TeamID:               " " + teamID + " ",
		Query:                " durable memory ",
		Limit:                500,
		KnownEvidenceIDs:     []string{" " + evidenceID + " ", evidenceID, ""},
		KnownRelationshipIDs: []string{relationshipID},
		ExpandFromEntityIDs:  []string{entityID, entityID},
	})

	if input.TeamID != teamID || input.Query != "durable memory" || input.Limit != maxV2RecallLimit {
		t.Fatalf("normalized input = %#v", input)
	}
	if len(input.KnownEvidenceIDs) != 1 || input.KnownEvidenceIDs[0] != evidenceID {
		t.Fatalf("known evidence = %#v", input.KnownEvidenceIDs)
	}
	if len(input.ExpandFromEntityIDs) != 1 || input.ExpandFromEntityIDs[0] != entityID {
		t.Fatalf("expand entities = %#v", input.ExpandFromEntityIDs)
	}
	if err := validateV2RecallEvidenceInput(input); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	missingQuery := input
	missingQuery.Query = ""
	missingQuery.ExpandFromEntityIDs = nil
	if err := validateV2RecallEvidenceInput(missingQuery); err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("missing query err = %v, want query validation", err)
	}

	badID := input
	badID.KnownRelationshipIDs = []string{"not-a-uuid"}
	if err := validateV2RecallEvidenceInput(badID); err == nil || !strings.Contains(err.Error(), "known_relationship_ids") {
		t.Fatalf("bad relationship id err = %v, want relationship id validation", err)
	}
}

func TestV2RecallBoundsAndContextHelpers(t *testing.T) {
	if got := v2RecallOverfetchLimit(1); got != v2RecallOverfetchFloor {
		t.Fatalf("overfetch floor = %d, want %d", got, v2RecallOverfetchFloor)
	}
	if got := v2RecallOverfetchLimit(100); got != v2RecallOverfetchCap {
		t.Fatalf("overfetch cap = %d, want %d", got, v2RecallOverfetchCap)
	}
	if got := v2RecallCombinedSearchState("", string(domain.V2SearchProjectionCurrent)); got != string(domain.V2SearchProjectionCurrent) {
		t.Fatalf("combined state = %q", got)
	}
	if got := v2RecallCombinedSearchState(string(domain.V2SearchProjectionCurrent), string(domain.V2SearchProjectionPending)); got != string(domain.V2SearchProjectionPending) {
		t.Fatalf("combined pending state = %q", got)
	}
	long := strings.Repeat("a", 2100)
	if got := truncateV2RecallContext(long); len(got) != 2000 {
		t.Fatalf("truncated length = %d, want 2000", len(got))
	}
}

func TestV2RecallANNHelpersUseDerivedContract(t *testing.T) {
	contractID := uuid.NewString()
	contract := &V2ActiveSearchContract{
		EmbeddingContractID: contractID,
		EmbeddingDimensions: 3072,
		IndexStrategy:       string(domain.V2VectorIndexHalfvecHNSW),
		CandidateLimit:      120,
	}
	expression, err := v2RecallANNDistanceExpression(contract)
	require.NoError(t, err)
	require.Equal(t, "embedding::halfvec(3072) <=> ?::halfvec(3072)", expression)

	literal, err := v2RecallEmbeddingContractLiteral(contractID)
	require.NoError(t, err)
	require.Equal(t, "'"+contractID+"'", literal)
	require.Equal(t, 120, v2RecallANNCandidateLimit(contract, 60))
	require.Equal(t, 80, v2RecallANNCandidateLimit(&V2ActiveSearchContract{CandidateLimit: 20}, 80))
	require.Equal(t, v2RecallOverfetchCap, v2RecallANNCandidateLimit(&V2ActiveSearchContract{CandidateLimit: 1000}, 80))

	_, err = v2RecallANNDistanceExpression(&V2ActiveSearchContract{
		EmbeddingDimensions: 5000,
		IndexStrategy:       string(domain.V2VectorIndexHalfvecHNSW),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrV2SearchContractMismatch), "err=%v", err)
}
