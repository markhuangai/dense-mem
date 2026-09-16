package service

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

func TestIdentityCharacterizationPreservesDistinctMentionGroundings(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	snapshot := cloneRememberAssessmentSnapshot(fixture.input.Snapshot)
	snapshot.Evidence[0].Content = "Alpha uses Beta. Alpha supports Gamma."
	snapshot.Items[0].Fragment.Content = snapshot.Evidence[0].Content
	snapshot.Proposal["relationship_hints"] = []any{
		map[string]any{
			"ref": "r:uses", "subject": map[string]any{"name": "Alpha", "entity_kind": "concept"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"entity": map[string]any{"name": "Beta", "entity_kind": "concept"}},
			"polarity":  "+", "evidence_indices": []any{0},
		},
		map[string]any{
			"ref": "r:supports", "subject": map[string]any{"name": "Alpha", "entity_kind": "concept"},
			"predicate": map[string]any{"proposed_key": "supports"},
			"object":    map[string]any{"entity": map[string]any{"name": "Gamma", "entity_kind": "concept"}},
			"polarity":  "+", "evidence_indices": []any{0},
		},
	}
	fixture.catalog.entityCandidates = map[string][]repository.SemanticReviewEntityCandidate{}
	fixture.catalog.predicateOptions = append(fixture.catalog.predicateOptions, repository.SemanticReviewPredicateCandidate{
		PredicateKey: "supports", Version: 1, AllowedSubjectKinds: []string{"concept"}, AllowedObjectKinds: []string{"concept"},
		RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active",
	})
	fixture.input = snapshotAsInput(snapshot)
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		response := validSynchronousAssessmentResponse(request)
		for index := range response.EntityResults {
			if response.EntityResults[index].Ref == "entity:1:subject" {
				for _, submitted := range request.SubmittedEntities {
					if submitted.Ref != response.EntityResults[index].Ref || len(submitted.Groundings) < 2 {
						continue
					}
					secondGrounding := submitted.Groundings[1].GroundingRef
					response.EntityResults[index].GroundingRef = &secondGrounding
					break
				}
			}
			response.EntityResults[index].Action = string(domain.EntityResolutionCreate)
			response.EntityResults[index].CandidateEntityID = nil
		}
		return response
	}

	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)

	commitInput, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{
		TeamID: fixture.input.Scope.TeamID, OwnerProfileID: fixture.input.Scope.OwnerProfileID, IngestID: fixture.input.Scope.IngestID,
		IdempotencyKey: "identity-characterization-unit", RequestHash: "identity-characterization-unit",
		Evidence:   []repository.EvidenceInput{{FragmentID: fixture.input.Snapshot.Evidence[0].FragmentID, Content: fixture.input.Snapshot.Evidence[0].Content, ContentHash: "unit-content", SourceType: "conversation", Authority: "primary"}, {FragmentID: fixture.input.Snapshot.Evidence[1].FragmentID, Content: fixture.input.Snapshot.Evidence[1].Content, ContentHash: "unit-content-2", SourceType: "conversation", Authority: "primary"}},
		Assessment: &SynchronousAssessmentResult{Response: prepared.Response, Request: prepared.Request, Plan: prepared.Plan, Assessment: prepared.Assessment},
	})
	require.NoError(t, err)

	alphaSpans := make([]string, 0, 2)
	for _, entry := range commitInput.Commit.EntityResolutions {
		if entry.Resolution.CanonicalName != "Alpha" {
			continue
		}
		require.Equal(t, string(domain.EntityResolutionCreate), entry.Resolution.Action)
		require.NotNil(t, entry.Resolution.SpanStart)
		require.NotNil(t, entry.Resolution.SpanEnd)
		alphaSpans = append(alphaSpans, fmt.Sprintf("%d:%d", *entry.Resolution.SpanStart, *entry.Resolution.SpanEnd))
	}
	sort.Strings(alphaSpans)
	require.Equal(t, []string{"0:5", "17:22"}, alphaSpans)
}

func TestIdentityCharacterizationTracksRepeatedNamesAcrossEvidence(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	snapshot := cloneRememberAssessmentSnapshot(fixture.input.Snapshot)
	snapshot.Evidence[0].Content = "Alpha uses Beta."
	snapshot.Evidence[1].Content = "Alpha supports Gamma."
	snapshot.Items[0].Fragment.Content = snapshot.Evidence[0].Content
	snapshot.Items[1].Fragment.Content = snapshot.Evidence[1].Content
	snapshot.Proposal["relationship_hints"] = []any{
		map[string]any{
			"ref": "r:uses", "subject": map[string]any{"name": "Alpha", "entity_kind": "concept"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"entity": map[string]any{"name": "Beta", "entity_kind": "concept"}},
			"polarity":  "+", "evidence_indices": []any{0},
		},
		map[string]any{
			"ref": "r:supports", "subject": map[string]any{"name": "Alpha", "entity_kind": "concept"},
			"predicate": map[string]any{"proposed_key": "supports"},
			"object":    map[string]any{"entity": map[string]any{"name": "Gamma", "entity_kind": "concept"}},
			"polarity":  "+", "evidence_indices": []any{1},
		},
	}
	fixture.catalog.entityCandidates = map[string][]repository.SemanticReviewEntityCandidate{}
	fixture.catalog.predicateOptions = append(fixture.catalog.predicateOptions, repository.SemanticReviewPredicateCandidate{
		PredicateKey: "supports", Version: 1, AllowedSubjectKinds: []string{"concept"}, AllowedObjectKinds: []string{"concept"},
		RelationshipKind: "state", CurrentCardinality: "many", LifecycleState: "active",
	})
	fixture.input = snapshotAsInput(snapshot)
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		response := validSynchronousAssessmentResponse(request)
		for index := range response.EntityResults {
			response.EntityResults[index].Action = string(domain.EntityResolutionCreate)
			response.EntityResults[index].CandidateEntityID = nil
		}
		return response
	}
	prepared, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
	require.NoError(t, err)
	commitInput, err := BuildSynchronousRememberCommitInput(SynchronousRememberCommitRequest{
		TeamID: fixture.input.Scope.TeamID, OwnerProfileID: fixture.input.Scope.OwnerProfileID, IngestID: fixture.input.Scope.IngestID,
		IdempotencyKey: "identity-characterization-unit-cross-evidence", RequestHash: "identity-characterization-unit-cross-evidence",
		Evidence:   []repository.EvidenceInput{{FragmentID: fixture.input.Snapshot.Evidence[0].FragmentID, Content: fixture.input.Snapshot.Evidence[0].Content, ContentHash: "unit-content", SourceType: "conversation", Authority: "primary"}, {FragmentID: fixture.input.Snapshot.Evidence[1].FragmentID, Content: fixture.input.Snapshot.Evidence[1].Content, ContentHash: "unit-content-2", SourceType: "conversation", Authority: "primary"}},
		Assessment: &SynchronousAssessmentResult{Response: prepared.Response, Request: prepared.Request, Plan: prepared.Plan, Assessment: prepared.Assessment},
	})
	require.NoError(t, err)

	alphaFragments := make([]string, 0, 2)
	for _, entry := range commitInput.Commit.EntityResolutions {
		if entry.Resolution.CanonicalName == "Alpha" {
			alphaFragments = append(alphaFragments, entry.Resolution.FragmentID)
		}
	}
	sort.Strings(alphaFragments)
	require.Len(t, alphaFragments, 2)
	require.NotEqual(t, alphaFragments[0], alphaFragments[1])
}

func TestIdentityCharacterizationKeepsSameNameCandidatesDistinct(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	firstID, secondID := uuid.NewString(), uuid.NewString()
	fixture.catalog.entityCandidates["entity:0:subject"] = []repository.SemanticReviewEntityCandidate{
		{EntityID: firstID, EntityKind: "concept", CanonicalName: "Alpha", ActiveNames: []string{"Alpha"}, Status: "active"},
		{EntityID: secondID, EntityKind: "concept", CanonicalName: "Alpha", ActiveNames: []string{"Alpha"}, Status: "active"},
	}

	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	engine := newAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID)
	request, err := engine.buildRequest(context.Background(), fixture.input.Scope, plan, fixture.input.Snapshot.Proposal)
	require.NoError(t, err)
	var subjectGroup assessor.SemanticAssessmentEntityCandidateGroup
	var subjectGrounding string
	for _, submitted := range request.SubmittedEntities {
		if submitted.Ref == "entity:0:subject" && len(submitted.Groundings) > 0 {
			subjectGrounding = submitted.Groundings[0].GroundingRef
			break
		}
	}
	require.NotEmpty(t, subjectGrounding)
	for _, group := range request.EntityCandidateGroups {
		if group.GroundingRef == subjectGrounding {
			subjectGroup = group
			break
		}
	}
	require.Len(t, subjectGroup.Candidates, 2)
	require.NotEqual(t, subjectGroup.Candidates[0].EntityID, subjectGroup.Candidates[1].EntityID)
}
