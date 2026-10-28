package recall

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
)

type recallDecisionFunction func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error)

func (f recallDecisionFunction) Decide(ctx context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	return f(ctx, request)
}

type snapshotReadFixture struct {
	*retrievalReadFixture
	insideSnapshot bool
}

func (f *snapshotReadFixture) WithEvidenceSnapshot(ctx context.Context, _ string, fn func(recallcontract.SearchRepository) error) error {
	f.insideSnapshot = true
	defer func() { f.insideSnapshot = false }()
	return fn(f.retrievalReadFixture)
}

func recallDecisionTestContext() context.Context {
	return context.WithValue(context.Background(), recallDecisionContextKey{}, recallDecisionSnapshot{operationID: "recall-operation", settings: domain.DecisionRuntimeConfig{Revision: "frozen-settings", Provider: domain.DecisionProviderStatus{Configured: true}, Effective: domain.DecisionTasks{RecallReranking: true}}})
}

func scoreAnswer(score float64) modelprovider.DecisionAnswer {
	confidence := 1.0
	return modelprovider.DecisionAnswer{Type: modelprovider.DecisionScore, Score: &score, Confidence: &confidence, Probabilities: map[string]float64{"0": 1 - score/3, "1": 0, "2": 0, "3": score / 3}}
}

func TestRecallDecisionFinalProvenanceMatchesCrossSpaceAndCommunitySelection(t *testing.T) {
	branch := &RecallResult{
		Results:              []RecallResultItem{{EvidenceID: "returned", Rank: 1}, {EvidenceID: "removed", Rank: 2}},
		RelatedRelationships: []RelatedRelationshipSummary{{RelationshipID: "relationship"}},
		DecisionStages: []modelprovider.DecisionStage{
			{References: map[string]string{"evidence_0": "returned", "evidence_1": "removed"}, SelectedIDs: []string{"returned", "removed"}},
			{References: map[string]string{"relationship_0": "relationship"}, SelectedIDs: []string{"relationship"}},
		},
	}
	final := fuseRecallResults([]*RecallResult{branch}, 1, 0)
	reconcileDecisionSelections(context.Background(), nil, final)
	require.Equal(t, []string{"returned"}, final.DecisionStages[0].SelectedIDs)
	require.Empty(t, final.DecisionStages[1].SelectedIDs)
	require.Len(t, FeedbackResultRefs(final), 1)
}

func TestRecallDecisionReranksAuthorizedHydratedShortlistOutsideSnapshot(t *testing.T) {
	team := uuid.NewString()
	fixture := &snapshotReadFixture{retrievalReadFixture: &retrievalReadFixture{evidence: map[string]recallcontract.RecallEvidenceHit{}, evidenceBatch: &recallcontract.RecallCandidateBatch{SearchState: "current"}}}
	ids := []string{}
	for index := 0; index < 60; index++ {
		id := uuid.NewString()
		ids = append(ids, id)
		fixture.evidenceBatch.TextHits = append(fixture.evidenceBatch.TextHits, searchcontract.SearchHit{SourceKind: "evidence", SourceID: id, SearchState: "current"})
		fixture.evidence[id] = recallcontract.RecallEvidenceHit{EvidenceID: id, TeamID: team, Context: fmt.Sprintf("authorized evidence %d", index)}
	}
	hidden := uuid.NewString()
	fixture.evidenceBatch.TextHits = append(fixture.evidenceBatch.TextHits, searchcontract.SearchHit{SourceKind: "evidence", SourceID: hidden})
	calls := 0
	provider := recallDecisionFunction(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		calls++
		require.False(t, fixture.insideSnapshot, "provider work cannot hold a PostgreSQL snapshot transaction")
		require.Len(t, request.Groups, 50)
		require.Equal(t, "frozen-settings", request.SettingsVersion)
		require.NotContains(t, fmt.Sprint(request.Groups), hidden)
		answers := map[string]modelprovider.DecisionAnswer{}
		for index, group := range request.Groups {
			score := 0.0
			if index >= 48 {
				score = 3
			}
			answers[group.Questions[0].Name] = scoreAnswer(score)
		}
		return modelprovider.DecisionResult{Answers: answers}, nil
	})
	retrieval := &Retrieval{repository: fixture, decisions: provider}
	result, err := retrieval.RecallEvidence(recallDecisionTestContext(), recallcontract.RecallEvidenceInput{TeamID: team, Query: "authorized", Limit: 2, OrganizationEnabled: true})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Len(t, result.Results, 2)
	require.Equal(t, ids[48], result.Results[0].EvidenceID)
	require.Equal(t, ids[49], result.Results[1].EvidenceID, "equal scores preserve original order")
	require.Equal(t, 1, result.Results[0].Rank)
	public := recallResultFromRepository(result, nil)
	refs := FeedbackResultRefs(public)
	require.Len(t, refs, 2)
	require.Equal(t, ids[48], refs[0].ID)
	require.Len(t, public.DecisionStages, 1)
}

func TestRecallDecisionFailuresKeepOriginalOrderAndCancellationFails(t *testing.T) {
	for _, failure := range []error{errors.New("provider 529"), context.Canceled, nil} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			fixture := &retrievalReadFixture{evidenceBatch: &recallcontract.RecallCandidateBatch{SearchState: "current"}, evidence: map[string]recallcontract.RecallEvidenceHit{}}
			ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
			for _, id := range ids {
				fixture.evidenceBatch.TextHits = append(fixture.evidenceBatch.TextHits, searchcontract.SearchHit{SourceKind: "evidence", SourceID: id})
				fixture.evidence[id] = recallcontract.RecallEvidenceHit{EvidenceID: id, Context: "authorized"}
			}
			provider := recallDecisionFunction(func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				return modelprovider.DecisionResult{}, failure
			})
			result, err := (&Retrieval{repository: fixture, decisions: provider}).RecallEvidence(recallDecisionTestContext(), recallcontract.RecallEvidenceInput{TeamID: uuid.NewString(), Query: "authorized", Limit: 2})
			if errors.Is(failure, context.Canceled) {
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Results, 2)
			require.Equal(t, ids[0], result.Results[0].EvidenceID)
			require.Equal(t, ids[1], result.Results[1].EvidenceID)
			require.Equal(t, "decision_reranking_unavailable", result.Degradations[0].Code)
		})
	}
}

func TestRecallDecisionRelationshipShortlistPreservesSemanticGrouping(t *testing.T) {
	fixture := &retrievalReadFixture{relationshipBatch: &recallcontract.RecallCandidateBatch{SearchState: "current"}, relationships: map[string]recallcontract.RecallRelationshipHit{}}
	ids := []string{}
	for index := 0; index < 25; index++ {
		id := uuid.NewString()
		ids = append(ids, id)
		fixture.relationshipBatch.TextHits = append(fixture.relationshipBatch.TextHits, searchcontract.SearchHit{SourceKind: "relationship", SourceID: id})
		fixture.relationships[id] = recallcontract.RecallRelationshipHit{RelationshipID: id, SemanticGroupKey: fmt.Sprintf("group_%d", index/2), SubjectName: "Atlas", PredicateKey: "uses", ObjectName: fmt.Sprint(index)}
	}
	provider := recallDecisionFunction(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		require.Len(t, request.Groups, 13)
		answers := map[string]modelprovider.DecisionAnswer{}
		for index, group := range request.Groups {
			score := 0.0
			if index == 12 {
				score = 3
			}
			answers[group.Questions[0].Name] = scoreAnswer(score)
		}
		return modelprovider.DecisionResult{Answers: answers}, nil
	})
	result, err := (&Retrieval{repository: fixture, decisions: provider}).RecallRelationships(recallDecisionTestContext(), recallcontract.RecallRelationshipsInput{TeamID: uuid.NewString(), Query: "Atlas uses", Limit: 1})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Equal(t, ids[24], result.Results[0].RelationshipID)
	require.Equal(t, 1, result.Results[0].Rank)
}

func TestRecallDecisionSelectionKeepsOnlyRelevantConflictMetadata(t *testing.T) {
	conflict := func(id string, evidence, relationships []string) tracecontract.RelationshipConflictCaseRecord {
		return tracecontract.RelationshipConflictCaseRecord{ConflictID: id, Positions: []domain.RelationshipConflictPositionRecord{{EvidenceIDs: evidence, RelationshipIDs: relationships}}}
	}
	result := &recallcontract.RecallEvidenceResult{
		Results: []recallcontract.RecallEvidenceHit{
			{EvidenceID: "dropped", RelationshipIDs: []string{"dropped-relationship"}},
			{EvidenceID: "selected", EquivalentEvidenceIDs: []string{"selected-alias"}, RelationshipIDs: []string{"selected-relationship"}},
		},
		Conflicts: []tracecontract.RelationshipConflictCaseRecord{
			conflict("by-evidence", []string{"selected"}, nil),
			conflict("by-alias", []string{"selected-alias"}, nil),
			conflict("by-relationship", nil, []string{"selected-relationship"}),
			conflict("dropped-conflict", []string{"dropped"}, []string{"dropped-relationship"}),
		},
		EvidenceConflicts: []recallcontract.EvidenceConflictCaseRecord{
			{ConflictID: "selected-conflict", Positions: []recallcontract.EvidenceConflictPositionRecord{{CanonicalEvidenceID: "selected"}}},
			{ConflictID: "alias-conflict", Positions: []recallcontract.EvidenceConflictPositionRecord{{CanonicalEvidenceID: "selected-alias"}}},
			{ConflictID: "removed-conflict", Positions: []recallcontract.EvidenceConflictPositionRecord{{CanonicalEvidenceID: "dropped"}}},
		},
	}
	provider := recallDecisionFunction(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		answers := map[string]modelprovider.DecisionAnswer{}
		for _, group := range request.Groups {
			score := 0.0
			if group.Reference == "selected" {
				score = 3
			}
			answers[group.Questions[0].Name] = scoreAnswer(score)
		}
		return modelprovider.DecisionResult{Answers: answers}, nil
	})
	selected, err := (&Retrieval{decisions: provider}).finishDecisionEvidence(recallDecisionTestContext(), recallcontract.RecallEvidenceInput{Query: "selected", Limit: 1}, result)
	require.NoError(t, err)
	require.Len(t, selected.Results, 1)
	require.Equal(t, "selected", selected.Results[0].EvidenceID)
	require.Equal(t, []string{"selected"}, selected.DecisionStages[0].SelectedIDs)
	require.Len(t, selected.Conflicts, 3)
	require.Equal(t, []string{"by-evidence", "by-alias", "by-relationship"}, []string{selected.Conflicts[0].ConflictID, selected.Conflicts[1].ConflictID, selected.Conflicts[2].ConflictID})
	require.Len(t, selected.EvidenceConflicts, 2)
	require.Equal(t, "selected-conflict", selected.EvidenceConflicts[0].ConflictID)
	require.Equal(t, "alias-conflict", selected.EvidenceConflicts[1].ConflictID)
}
