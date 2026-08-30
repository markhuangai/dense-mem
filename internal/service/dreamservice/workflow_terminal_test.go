package dreamservice

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestResolveFeedbackUsesExplicitRememberResultKind(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	baseRecord := repository.HypothesisRecord{
		TeamID:             teamID.String(),
		HypothesisID:       hypothesisID,
		CreatedByProfileID: ownerID.String(),
		Status:             string(domain.DreamStatusProposed),
		Statement:          "Dense-Mem may use PostgreSQL.",
	}

	tests := []struct {
		name         string
		result       *rememberapp.RememberResult
		rememberErr  error
		processError bool
		wantStatus   domain.DreamStatus
		wantMemory   bool
		wantSubmit   bool
		wantError    string
	}{
		{
			name: "legacy receipt preserves queued behavior",
			result: func() *rememberapp.RememberResult {
				ingestID := uuid.NewString()
				return &rememberapp.RememberResult{
					IngestID: ingestID, SubmissionID: ingestID,
					ProcessingState: string(domain.PlacementRunQueued),
					Kind:            rememberapp.ResultKindLegacyReceipt,
				}
			}(),
			wantStatus: domain.DreamStatusSubmitted, wantSubmit: true,
		},
		{
			name:       "terminal completed submits committed ingest",
			result:     dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), uuid.NewString()),
			wantStatus: domain.DreamStatusSubmitted,
			wantSubmit: true,
		},
		{
			name:       "terminal rejected stays reviewable",
			result:     dreamTerminalRememberResult(string(rememberapp.TerminalProcessingRejected), uuid.NewString()),
			wantStatus: domain.DreamStatusProposed, wantMemory: true,
		},
		{
			name:       "terminal quarantined stays reviewable",
			result:     dreamTerminalRememberResult(string(rememberapp.TerminalProcessingQuarantined), uuid.NewString()),
			wantStatus: domain.DreamStatusProposed, wantMemory: true,
		},
		{
			name:         "operational terminal failure stays reviewable",
			result:       dreamTerminalRememberResult(string(rememberapp.TerminalProcessingFailed), uuid.NewString()),
			processError: true,
			wantStatus:   domain.DreamStatusProposed,
			wantMemory:   true,
		},
		{
			name:      "unknown kind fails closed",
			result:    &rememberapp.RememberResult{Kind: rememberapp.ResultKind("future")},
			wantError: "Remember result kind is required",
		},
		{
			name:      "terminal result is required",
			result:    &rememberapp.RememberResult{Kind: rememberapp.ResultKindTerminal},
			wantError: "terminal Remember result is required",
		},
		{
			name:      "completed result needs canonical ingest",
			result:    dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), "not-a-uuid"),
			wantError: "completed Remember result has no canonical ingest",
		},
		{
			name:        "process error without terminal result propagates",
			result:      nil,
			rememberErr: &rememberapp.RememberProcessError{Result: nil, Err: errors.New("provider unavailable")},
			wantError:   "remember: processor failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dreamRepositoryStub{getRecord: baseRecord}
			rememberErr := tc.rememberErr
			if tc.processError {
				rememberErr = &rememberapp.RememberProcessError{Result: tc.result.Terminal, Err: errors.New("provider unavailable")}
			}
			remember := &rememberServiceStub{result: tc.result, err: rememberErr}
			svc := New(Dependencies{Store: repo, Remember: remember})
			result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
				DreamID: hypothesisID, Decision: "confirm_true",
				Evidence: []rememberapp.RememberEvidenceInput{{Content: "An independent deployment note."}},
			})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Empty(t, repo.submitInput)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.Dream)
			require.Equal(t, tc.wantStatus, result.Dream.Status)
			if tc.wantMemory {
				require.NotNil(t, result.Memory)
			}
			if tc.wantSubmit {
				require.NotEmpty(t, repo.submitInput.SubmittedIngestID)
			} else {
				require.Empty(t, repo.submitInput)
			}
		})
	}
}

func TestResolveFeedbackReplaysCompletedRememberResult(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	repo := &dreamRepositoryStub{getRecord: repository.HypothesisRecord{
		TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(),
		Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
	}}
	remember := &rememberServiceStub{result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), ingestID)}
	svc := New(Dependencies{Store: repo, Remember: remember})
	request := ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true", IdempotencyKey: "dream-replay",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "An independent deployment note."}},
	}

	first, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", request)
	require.NoError(t, err)
	// A completed confirmation changes the durable Hypothesis status. The
	// retry payload must remain identical so Remember can replay its terminal
	// result under the same idempotency key.
	repo.getRecord.Status = string(domain.DreamStatusSubmitted)
	second, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", request)
	require.NoError(t, err)
	require.Equal(t, domain.DreamStatusSubmitted, first.Dream.Status)
	require.Equal(t, domain.DreamStatusSubmitted, second.Dream.Status)
	require.Equal(t, ingestID, first.Memory.IngestID)
	require.Equal(t, ingestID, second.Memory.IngestID)
	require.Len(t, remember.requests, 2)
	require.Equal(t, request.IdempotencyKey, remember.requests[0].IdempotencyKey)
	require.Equal(t, request.IdempotencyKey, remember.requests[1].IdempotencyKey)
	require.Equal(t, remember.requests[0].Evidence, remember.requests[1].Evidence)
}

func dreamTerminalRememberResult(state, submissionID string) *rememberapp.RememberResult {
	terminal := &rememberapp.TerminalRememberResult{
		ContractVersion: domain.ContractVersion,
		SubmissionID:    submissionID,
		SubmissionKind:  "remember",
		ProcessingState: state,
		CorrelationID:   "dream-test-correlation",
		Kind:            rememberapp.ResultKindTerminal,
	}
	return &rememberapp.RememberResult{
		ContractVersion: terminal.ContractVersion,
		IngestID:        terminal.SubmissionID,
		SubmissionID:    terminal.SubmissionID,
		SubmissionKind:  terminal.SubmissionKind,
		ProcessingState: terminal.ProcessingState,
		CorrelationID:   terminal.CorrelationID,
		Kind:            rememberapp.ResultKindTerminal,
		Terminal:        terminal,
	}
}
