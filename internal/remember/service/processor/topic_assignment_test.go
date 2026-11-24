package processor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/topicassignment"
	assignment "github.com/markhuangai/dense-mem/internal/topicassignment/contract"
	"github.com/stretchr/testify/require"
)

type topicIntakeFixture struct {
	scopes    []assignment.Scope
	slices    []assignment.Slice
	readError error
	quote     string
}

func (f *topicIntakeFixture) TopicMaintenanceRuntimeConfig(context.Context) (domain.TopicMaintenanceConfig, error) {
	return domain.TopicMaintenanceConfig{Enabled: true, ContextWindowTokens: 500000}, nil
}

func (f *topicIntakeFixture) ReadAssignmentCandidates(_ context.Context, scope assignment.Scope, slice assignment.Slice) (assignment.Candidates, error) {
	f.scopes = append(f.scopes, scope)
	f.slices = append(f.slices, slice)
	return assignment.Candidates{}, f.readError
}

func (f *topicIntakeFixture) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
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
		{name: "stale reuse", target: domain.MemorySpaceTeamShared, missingCanonical: true, want: remember.ErrRememberStaleInput},
		{name: "catalog failure", target: domain.MemorySpaceTeamShared, readError: storageFailure, want: remember.ErrRememberDatabaseFailure},
		{name: "ungrounded generation", quote: "submitted paraphrase", target: domain.MemorySpaceTeamShared, want: remember.ErrRememberProviderResponseInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &topicIntakeFixture{quote: tc.quote, readError: tc.readError}
			service := topicassignment.NewService(fixture, fixture, fixture, nil, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
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
			err := processor.prepareTopics(t.Context(), tc.target, &input, groups, nil)
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
			require.Equal(t, []assignment.Slice{{Index: 0, Text: canonical}}, fixture.slices)
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
