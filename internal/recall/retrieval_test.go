package recall

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

type retrievalReadFixture struct {
	evidenceBatch     *recallcontract.RecallCandidateBatch
	relationshipBatch *recallcontract.RecallCandidateBatch
	evidence          map[string]recallcontract.RecallEvidenceHit
	relationships     map[string]recallcontract.RecallRelationshipHit
	conflicts         *recallcontract.RecallConflicts
	candidateError    error
	hydrationError    error
	conflictError     error
	evidenceInput     recallcontract.RecallEvidenceInput
	relationshipInput recallcontract.RecallRelationshipsInput
	candidateLimit    int
	evidenceReads     int
	relationshipReads int
	hydrationReads    int
	conflictReads     int
}

func (f *retrievalReadFixture) GetActiveSearchContract(context.Context) (*searchcontract.ActiveSearchContract, error) {
	return &searchcontract.ActiveSearchContract{}, nil
}
func (f *retrievalReadFixture) ReadEvidenceCandidates(_ context.Context, input recallcontract.RecallEvidenceInput, _ *searchcontract.ActiveSearchContract, limit int) (*recallcontract.RecallCandidateBatch, error) {
	f.evidenceReads++
	f.evidenceInput = input
	f.candidateLimit = limit
	return f.evidenceBatch, f.candidateError
}
func (f *retrievalReadFixture) ReadRelationshipCandidates(_ context.Context, input recallcontract.RecallRelationshipsInput, _ *searchcontract.ActiveSearchContract, limit int) (*recallcontract.RecallCandidateBatch, error) {
	f.relationshipReads++
	f.relationshipInput = input
	f.candidateLimit = limit
	return f.relationshipBatch, f.candidateError
}
func (f *retrievalReadFixture) HydrateEvidence(context.Context, recallcontract.RecallEvidenceInput, *searchcontract.ActiveSearchContract, []string) (map[string]recallcontract.RecallEvidenceHit, error) {
	f.hydrationReads++
	return f.evidence, f.hydrationError
}
func (f *retrievalReadFixture) HydrateRelationships(context.Context, recallcontract.RecallRelationshipsInput, *searchcontract.ActiveSearchContract, []string) (map[string]recallcontract.RecallRelationshipHit, error) {
	f.hydrationReads++
	return f.relationships, f.hydrationError
}
func (f *retrievalReadFixture) LoadRecallConflicts(context.Context, recallcontract.RecallEvidenceInput, []recallcontract.RecallEvidenceHit) (*recallcontract.RecallConflicts, error) {
	f.conflictReads++
	if f.conflicts == nil {
		return &recallcontract.RecallConflicts{}, f.conflictError
	}
	return f.conflicts, f.conflictError
}

func TestRetrievalFusesEvidenceAtOriginalBranchPositions(t *testing.T) {
	team := uuid.NewString()
	known := uuid.NewString()
	a := uuid.NewString()
	b := uuid.NewString()
	fixture := &retrievalReadFixture{
		evidenceBatch: &recallcontract.RecallCandidateBatch{
			SearchState: string(domain.SearchProjectionCurrent),
			TextHits: []searchcontract.SearchHit{
				{SourceKind: "evidence", SourceID: known, SearchState: "current"},
				{SourceKind: "evidence", SourceID: b, SearchState: "failed"},
				{SourceKind: "evidence", SourceID: a, SearchState: "pending"},
			},
			VectorHits: []searchcontract.SearchHit{
				{SourceKind: "evidence", SourceID: a, SearchState: "current"},
				{SourceKind: "evidence", SourceID: b, SearchState: "current"},
			},
			ExpansionHits: []searchcontract.SearchHit{
				{SourceKind: "relationship", SourceID: uuid.NewString()},
				{SourceKind: "evidence", SourceID: a, SearchState: "current"},
			},
		},
		evidence: map[string]recallcontract.RecallEvidenceHit{
			a: {TeamID: team, EvidenceID: a, Context: "A", SearchState: "current"},
			b: {TeamID: team, EvidenceID: b, Context: "B", SearchState: "current"},
		},
	}
	result, err := NewRetrieval(fixture).RecallEvidence(t.Context(), recallcontract.RecallEvidenceInput{
		TeamID: team, Query: "  memory  ", KnownEvidenceIDs: []string{known, known},
		Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{a, b}, []string{result.Results[0].EvidenceID, result.Results[1].EvidenceID})
	require.Equal(t, []int{1, 2}, []int{result.Results[0].Rank, result.Results[1].Rank})
	require.InDelta(t, 1.0/63+1.0/61+0.5/62, result.Results[0].Score, 1e-12)
	require.Equal(t, "pending", result.Results[0].SearchState)
	require.Equal(t, "failed", result.SearchState)
	require.Equal(t, "memory", fixture.evidenceInput.Query)
	require.Equal(t, []string{known}, fixture.evidenceInput.KnownEvidenceIDs)
	require.Equal(t, 60, fixture.candidateLimit)
	require.Equal(t, 1, fixture.hydrationReads)
	require.Equal(t, 1, fixture.conflictReads)
}

func TestRetrievalPreservesRelationshipRepresentativeAndFinalAgeOrder(t *testing.T) {
	team := uuid.NewString()
	a, b, duplicate := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if a > b {
		a, b = b, a
	}
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	fixture := &retrievalReadFixture{
		relationshipBatch: &recallcontract.RecallCandidateBatch{
			SearchState: "current",
			TextHits: []searchcontract.SearchHit{
				{SourceKind: "relationship", SourceID: a, SearchState: "current"},
				{SourceKind: "relationship", SourceID: b, SearchState: "current"},
				{SourceKind: "relationship", SourceID: duplicate, SearchState: "current"},
			},
			VectorHits: []searchcontract.SearchHit{
				{SourceKind: "relationship", SourceID: b, SearchState: "current"},
				{SourceKind: "relationship", SourceID: a, SearchState: "current"},
			},
		},
		relationships: map[string]recallcontract.RecallRelationshipHit{
			a:         {TeamID: team, RelationshipID: a, SemanticGroupKey: "one", CreatedAt: newer, SearchState: "current"},
			b:         {TeamID: team, RelationshipID: b, SemanticGroupKey: "two", CreatedAt: older, SearchState: "current"},
			duplicate: {TeamID: team, RelationshipID: duplicate, SemanticGroupKey: "one", CreatedAt: older, SearchState: "current"},
		},
	}
	result, err := NewRetrieval(fixture).RecallRelationships(t.Context(), recallcontract.RecallRelationshipsInput{
		TeamID: team, Query: "relationship", Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{b, a}, []string{result.Results[0].RelationshipID, result.Results[1].RelationshipID})
	require.Equal(t, []int{1, 2}, []int{result.Results[0].Rank, result.Results[1].Rank})
	require.Equal(t, result.Results[0].Score, result.Results[1].Score)
	require.False(t, result.VectorOmitted)
	require.Equal(t, 60, fixture.candidateLimit)
}

func TestRetrievalTieBreakers(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()
	if a > b {
		a, b = b, a
	}
	evidence := fuseRecallCandidates(&recallcontract.RecallCandidateBatch{
		TextHits: []searchcontract.SearchHit{
			{SourceKind: "evidence", SourceID: b},
			{SourceKind: "evidence", SourceID: a},
		},
		VectorHits: []searchcontract.SearchHit{
			{SourceKind: "evidence", SourceID: a},
			{SourceKind: "evidence", SourceID: b},
		},
	}, "evidence", nil)
	require.Equal(t, []string{a, b}, recallCandidateIDs(evidence))
	require.Equal(t, evidence[0].Score, evidence[1].Score)

	relationships := []recallCandidate{
		{ID: a, Score: 1, BestBranchRank: 2},
		{ID: b, Score: 1, BestBranchRank: 1},
	}
	sortRecallCandidates(relationships, "relationship")
	require.Equal(t, []string{b, a}, recallCandidateIDs(relationships))
	relationships[1].BestBranchRank = 1
	sortRecallCandidates(relationships, "relationship")
	require.Equal(t, []string{a, b}, recallCandidateIDs(relationships))

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	results := []recallcontract.RecallRelationshipHit{
		{RelationshipID: b, Score: 1, CreatedAt: created},
		{RelationshipID: a, Score: 1, CreatedAt: created},
	}
	sortRecallRelationshipResults(results)
	require.Equal(t, []string{a, b}, []string{results[0].RelationshipID, results[1].RelationshipID})
}

func TestRetrievalSelectsDistinctSemanticGroupsBeforeLimit(t *testing.T) {
	team := uuid.NewString()
	first, duplicate, second := uuid.NewString(), uuid.NewString(), uuid.NewString()
	fixture := &retrievalReadFixture{
		relationshipBatch: &recallcontract.RecallCandidateBatch{
			SearchState: "current",
			TextHits: []searchcontract.SearchHit{
				{SourceKind: "relationship", SourceID: first},
				{SourceKind: "relationship", SourceID: duplicate},
				{SourceKind: "relationship", SourceID: second},
			},
		},
		relationships: map[string]recallcontract.RecallRelationshipHit{
			first:     {RelationshipID: first, SemanticGroupKey: "same"},
			duplicate: {RelationshipID: duplicate, SemanticGroupKey: "same"},
			second:    {RelationshipID: second, SemanticGroupKey: "distinct"},
		},
	}
	result, err := NewRetrieval(fixture).RecallRelationships(t.Context(), recallcontract.RecallRelationshipsInput{
		TeamID: team, Query: "groups", Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{first, second}, []string{result.Results[0].RelationshipID, result.Results[1].RelationshipID})
	firstScore := 1.0 / 61
	secondScore := 1.0 / 63
	require.InDelta(t, firstScore, result.Results[0].Score, 1e-12)
	require.InDelta(t, secondScore, result.Results[1].Score, 1e-12)
}

func TestRetrievalBoundsAndRequiredReadFailures(t *testing.T) {
	team := uuid.NewString()
	id := uuid.NewString()
	for _, stage := range []string{"candidate", "hydration", "conflict"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New(stage + " failed")
			fixture := &retrievalReadFixture{
				evidenceBatch: &recallcontract.RecallCandidateBatch{
					SearchState: "current", TextHits: []searchcontract.SearchHit{{SourceKind: "evidence", SourceID: id}},
				},
				evidence: map[string]recallcontract.RecallEvidenceHit{id: {EvidenceID: id}},
			}
			switch stage {
			case "candidate":
				fixture.candidateError = failure
			case "hydration":
				fixture.hydrationError = failure
			case "conflict":
				fixture.conflictError = failure
			}
			result, err := NewRetrieval(fixture).RecallEvidence(t.Context(), recallcontract.RecallEvidenceInput{
				TeamID: team, Query: "query", Limit: 50,
			})
			require.Nil(t, result)
			require.ErrorIs(t, err, failure)
			require.Equal(t, recallcontract.MaxRecallCandidateCount, fixture.candidateLimit)
		})
	}
	fixture := &retrievalReadFixture{}
	_, err := NewRetrieval(fixture).RecallEvidence(t.Context(), recallcontract.RecallEvidenceInput{
		TeamID: team, Query: "query", SpaceKind: string(domain.MemorySpaceCredentialPrivate),
	})
	require.ErrorContains(t, err, "space_id is required")
	require.Zero(t, fixture.evidenceReads)
	_, err = NewRetrieval(fixture).RecallEvidence(t.Context(), recallcontract.RecallEvidenceInput{
		TeamID: team, Query: "query", KnownRelationshipIDs: []string{"invalid"},
	})
	require.ErrorContains(t, err, "known_relationship_ids")
	require.Zero(t, fixture.evidenceReads)
	require.Equal(t, 60, recallOverfetchLimit(1))
	require.Equal(t, recallcontract.MaxRecallCandidateCount, recallOverfetchLimit(50))
}
