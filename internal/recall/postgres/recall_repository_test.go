package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
)

func TestRecallSpacePredicateFailsClosedForOmittedOrUnknownSpace(t *testing.T) {
	teamID := uuid.NewString()
	teamShared := recallSpacePredicate("fragment.space_id", teamID, "", "")
	if !strings.Contains(teamShared, "dense_mem_team_shared_space") {
		t.Fatalf("omitted space predicate = %q, want team_shared predicate", teamShared)
	}
	if got := recallSpacePredicate("fragment.space_id", teamID, "", "not-a-space-kind"); !strings.Contains(got, "FALSE") {
		t.Fatalf("unknown space predicate = %q, want fail-closed FALSE", got)
	}
}

func TestRecallRelationshipEvidenceOverlapFiltersKnownSupport(t *testing.T) {
	evidenceID := uuid.NewString()
	otherEvidenceID := uuid.NewString()
	if !recallEvidenceOverlaps([]string{otherEvidenceID, evidenceID}, []string{evidenceID}) {
		t.Fatal("expected known evidence overlap")
	}
	if recallEvidenceOverlaps([]string{otherEvidenceID}, []string{evidenceID}) {
		t.Fatal("unexpected evidence overlap")
	}
}

func TestRecallBoundsAndContextHelpers(t *testing.T) {
	if got := domain.CombineSearchProjectionStates("", string(domain.SearchProjectionCurrent)); got != string(domain.SearchProjectionCurrent) {
		t.Fatalf("combined state = %q", got)
	}
	if got := domain.CombineSearchProjectionStates(string(domain.SearchProjectionCurrent), string(domain.SearchProjectionPending)); got != string(domain.SearchProjectionPending) {
		t.Fatalf("combined pending state = %q", got)
	}
	if got := domain.CombineSearchProjectionStates(string(domain.SearchProjectionFailed), string(domain.SearchProjectionPending)); got != string(domain.SearchProjectionFailed) {
		t.Fatalf("failed state was downgraded = %q", got)
	}
	if got := domain.CombineSearchProjectionStates(string(domain.SearchProjectionNotRequired), string(domain.SearchProjectionCurrent)); got != string(domain.SearchProjectionCurrent) {
		t.Fatalf("current state was downgraded = %q", got)
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
	require.Equal(t, recallcontract.MaxRecallCandidateCount, recallANNCandidateLimit(&ActiveSearchContract{CandidateLimit: 1000}, 80))

	binaryContract := &ActiveSearchContract{
		EmbeddingDimensions: 4096,
		IndexStrategy:       string(domain.VectorIndexBinaryHNSW),
	}
	binaryExpression, err := recallANNDistanceExpression(binaryContract)
	require.NoError(t, err)
	require.Equal(t, "binary_quantize(embedding)::bit(4096) <~> binary_quantize(?::vector)::bit(4096)", binaryExpression)

	_, err = recallANNDistanceExpression(&ActiveSearchContract{
		EmbeddingDimensions: 5000,
		IndexStrategy:       string(domain.VectorIndexHalfvecHNSW),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSearchContractMismatch), "err=%v", err)
}

func TestRecallANNQueryEFSearchCoversCandidateLimit(t *testing.T) {
	require.Equal(t, 200, recallANNQueryEFSearch(&ActiveSearchContract{QueryEFSearch: 40}, 200))
	require.Equal(t, 240, recallANNQueryEFSearch(&ActiveSearchContract{QueryEFSearch: 240}, 200))
	require.Equal(t, searchDefaultQueryEFSearch, recallANNQueryEFSearch(nil, 0))
}
