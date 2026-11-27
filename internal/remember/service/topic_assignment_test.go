package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/topics"
	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
	"github.com/stretchr/testify/require"
)

type topicIntakeFixture struct {
	scopes       []assignment.Scope
	slices       []assignment.Slice
	readError    error
	quote        string
	contextLimit int64
	dispatched   atomic.Int32
}

func (f *topicIntakeFixture) TopicMaintenanceRuntimeConfig(context.Context) (domain.TopicMaintenanceConfig, error) {
	window := f.contextLimit
	if window == 0 {
		window = 500000
	}
	return domain.TopicMaintenanceConfig{Enabled: true, ContextWindowTokens: window}, nil
}

func (f *topicIntakeFixture) ReadAssignmentCandidates(_ context.Context, scope assignment.Scope, slice assignment.Slice) (assignment.Candidates, error) {
	f.scopes = append(f.scopes, scope)
	f.slices = append(f.slices, slice)
	return assignment.Candidates{}, f.readError
}

func (f *topicIntakeFixture) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	f.dispatched.Add(1)
	var input struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	body, err := json.Marshal(map[string]any{
		"request_id": input.RequestID, "status": "assigned", "matches": []any{},
		"proposals": []assignment.Proposal{{Label: "Recovery", Description: "Database recovery", Quote: f.quote}},
	})
	return modelprovider.StructuredResult{Content: string(body)}, err
}

func TestRememberTopicBudgetFailureGuidesOperator(t *testing.T) {
	limits := assessor.DefaultSemanticAssessmentLimits()
	fixture := &topicIntakeFixture{contextLimit: int64(limits.MaxOutputTokens) + 1}
	service := topics.NewAssignmentService(fixture, fixture, fixture, nil, limits, "fixture", nil)
	processor := &rememberSynchronousProcessor{topicAssignment: service}
	input := knowledge.SynchronousRememberCommitInput{
		TeamID: uuid.NewString(), SpaceID: uuid.NewString(), SpaceGeneration: 1, IngestID: uuid.NewString(),
		Evidence: []knowledge.EvidenceInput{{Content: "PostgreSQL backups record the last known restore point."}},
	}
	err := processor.prepareTopics(t.Context(), domain.MemorySpaceTeamShared, &input, nil, nil, nil, nil, nil)
	require.ErrorIs(t, err, ErrRememberInputBudgetExceeded)
	require.Empty(t, input.TopicAssignments)
	require.Zero(t, fixture.dispatched.Load())
	code := rememberFailureCode("assessment", err)
	reason, details := RememberFailureDetails(err, "assessment")
	failure := TerminalStatusErrorWithDetails(TerminalErrorCode(code), reason, details)
	require.Equal(t, string(TerminalErrorInputBudgetExceeded), failure.Code)
	require.False(t, failure.Retryable)
	require.Equal(t, string(SubmissionNextActionContactOperator), failure.NextAction)
	require.Equal(t, true, failure.Details["server_owned"])
}

func TestRememberTopicPreparationUsesCanonicalEvidenceAndFailsAtomically(t *testing.T) {
	const canonical = "PostgreSQL recovery drills record the latest restore point."
	storageFailure := errors.New("topic catalog unavailable")
	for _, tc := range []struct {
		name, quote      string
		target           domain.MemorySpaceKind
		missingCanonical bool
		readError, want  error
	}{
		{name: "shared", quote: "recovery drills", target: domain.MemorySpaceTeamShared},
		{name: "private", target: domain.MemorySpacePrivate},
		{name: "stale reuse", target: domain.MemorySpaceTeamShared, missingCanonical: true, want: ErrRememberStaleInput},
		{name: "stale candidate", target: domain.MemorySpaceTeamShared, readError: assignment.ErrStale, want: ErrRememberStaleInput},
		{name: "catalog failure", target: domain.MemorySpaceTeamShared, readError: storageFailure, want: ErrRememberDatabaseFailure},
		{name: "ungrounded generation", quote: "submitted paraphrase", target: domain.MemorySpaceTeamShared, want: ErrRememberProviderResponseInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &topicIntakeFixture{quote: tc.quote, readError: tc.readError}
			service := topics.NewAssignmentService(fixture, fixture, fixture, nil, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
			processor := &rememberSynchronousProcessor{topicAssignment: service}
			id := uuid.NewString()
			input := knowledge.SynchronousRememberCommitInput{
				TeamID: uuid.NewString(), SpaceID: uuid.NewString(), SpaceGeneration: 2, IngestID: uuid.NewString(),
				Evidence: []knowledge.EvidenceInput{
					{Content: "A submitted paraphrase of the restore note."},
					{Content: "Quarantined content.", InitialEvent: &knowledge.SecurityEventDraft{Decision: "quarantine"}},
				},
				DuplicateResolutions: []knowledge.RememberDuplicateResolution{{EvidenceIndex: 0, CandidateFragmentID: id, Disposition: "reuse"}},
			}
			groups := []knowledge.RememberDuplicateCandidateGroup{{Candidates: []knowledge.RememberDuplicateCandidate{{FragmentID: id, Content: canonical}}}}
			if tc.missingCanonical {
				groups = nil
			}
			err := processor.prepareTopics(t.Context(), tc.target, &input, groups, nil, nil, nil, nil)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
				require.Empty(t, input.TopicAssignments)
			} else {
				require.NoError(t, err)
			}
			if tc.target == domain.MemorySpacePrivate || tc.missingCanonical {
				require.Empty(t, fixture.slices)
				require.Empty(t, input.TopicAssignments)
				return
			}
			require.Equal(t, []assignment.Slice{{Index: 0, Text: canonical, CanonicalEvidenceID: id}}, fixture.slices)
			require.Equal(t, []assignment.Scope{{TeamID: input.TeamID, SpaceID: input.SpaceID, Generation: 2, OperationID: input.IngestID}}, fixture.scopes)
			if tc.want == nil {
				require.Len(t, input.TopicAssignments, 1)
				require.Equal(t, 0, input.TopicAssignments[0].Index)
				require.Equal(t, canonical, input.TopicAssignments[0].Text)
				require.Equal(t, tc.quote, input.TopicAssignments[0].Proposals[0].Quote)
			}
		})
	}
}

func TestRememberTopicPreparationReusesOnlyCanonicalEmbeddings(t *testing.T) {
	fixture := &topicIntakeFixture{quote: "evidence"}
	service := topics.NewAssignmentService(fixture, fixture, fixture, nil, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	processor := &rememberSynchronousProcessor{topicAssignment: service}
	semanticID, exactID := uuid.NewString(), uuid.NewString()
	input := knowledge.SynchronousRememberCommitInput{
		TeamID: uuid.NewString(), SpaceID: uuid.NewString(), SpaceGeneration: 1, IngestID: uuid.NewString(),
		Evidence: []knowledge.EvidenceInput{
			{Content: "New evidence"},
			{Content: "Submitted evidence paraphrase"},
			{Content: "Exact evidence"},
			{Content: "Quarantined evidence", InitialEvent: &knowledge.SecurityEventDraft{Decision: "quarantine"}},
		},
		DuplicateResolutions: []knowledge.RememberDuplicateResolution{
			{EvidenceIndex: 1, CandidateFragmentID: semanticID, Disposition: "reuse"},
			{EvidenceIndex: 2, CandidateFragmentID: exactID, Disposition: "reuse", Exact: true},
		},
	}
	candidates := []knowledge.RememberDuplicateCandidateGroup{{Candidates: []knowledge.RememberDuplicateCandidate{{FragmentID: semanticID, Content: "Canonical evidence"}}}}
	documents := []knowledge.SearchDocumentForEmbedding{
		{DocumentText: "New evidence", DocumentHash: "new"},
		{DocumentText: "Submitted evidence paraphrase", DocumentHash: "paraphrase"},
	}
	active := &knowledge.InlineEmbeddingPlan{EmbeddingContractID: "contract", SearchIndexGenerationID: "generation", IndexGeneration: 1}
	embeddings := []knowledge.InlineEmbeddingResult{
		{DocumentHash: "new", Embedding: []float32{1, 0}, EmbeddingContractID: "contract", SearchIndexGenerationID: "generation", IndexGeneration: 1},
		{DocumentHash: "paraphrase", Embedding: []float32{0, 1}, EmbeddingContractID: "contract", SearchIndexGenerationID: "generation", IndexGeneration: 1},
	}
	require.NoError(t, processor.prepareTopics(t.Context(), domain.MemorySpaceTeamShared, &input, candidates, documents, embeddings, active, nil))
	require.Len(t, fixture.slices, 3)
	require.Equal(t, []float32{1, 0}, fixture.slices[0].Embedding)
	require.Equal(t, "contract", fixture.slices[0].EmbeddingContractID)
	require.Equal(t, "Canonical evidence", fixture.slices[1].Text)
	require.Equal(t, semanticID, fixture.slices[1].CanonicalEvidenceID)
	require.Empty(t, fixture.slices[1].Embedding)
	require.Equal(t, exactID, fixture.slices[2].CanonicalEvidenceID)
	require.Empty(t, fixture.slices[2].Embedding)
}
