package dreamservice

import (
	"context"
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

func TestResolveFeedbackRetriesHypothesisFinalizationAfterCancellation(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	ctx, cancel := context.WithCancel(dreamTestContext(teamID, ownerID))
	defer cancel()
	repo := &dreamRepositoryStub{
		getRecord: repository.HypothesisRecord{
			TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(),
			Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
		},
		submitErrs: []error{context.Canceled, nil},
	}
	remember := &rememberServiceStub{
		result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), ingestID),
		after:  cancel,
	}
	svc := New(Dependencies{Store: repo, Remember: remember})

	result, err := svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, domain.DreamStatusSubmitted, result.Dream.Status)
	require.Equal(t, ingestID, result.Memory.IngestID)
	require.Equal(t, 2, repo.submitCalls)
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
	repo.getRecord.SubmittedIngestID = ingestID
	repo.getRecord.SubmittedIngestIdempotencyKey = request.IdempotencyKey
	repo.getRecord.SubmittedDecision = request.Decision
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

func TestResolveFeedbackReplaysPreUpgradeSubmittedRememberWithoutCallingRemember(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	record := repository.HypothesisRecord{
		TeamID:                        teamID.String(),
		HypothesisID:                  hypothesisID,
		CreatedByProfileID:            ownerID.String(),
		Status:                        string(domain.DreamStatusSubmitted),
		Statement:                     "Dense-Mem may use PostgreSQL.",
		InvalidatedReason:             "legacy confirmation reason",
		SubmittedIngestID:             ingestID,
		SubmittedIngestIdempotencyKey: "legacy-dream-replay",
		SubmittedDecision:             "confirm_true",
	}
	request := ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true", Feedback: record.InvalidatedReason,
		IdempotencyKey: record.SubmittedIngestIdempotencyKey,
		Evidence: []rememberapp.RememberEvidenceInput{{
			Content:  "Independent legacy deployment evidence.",
			Metadata: map[string]any{"dream_feedback_reason": "legacy metadata reason"},
		}},
	}
	legacyEvidence, err := dreamSubmissionEvidenceWithStatus(request, &record, string(domain.DreamStatusProposed), true)
	require.NoError(t, err)
	record.SubmittedIngestRequestHash, err = rememberapp.CanonicalRequestBodyHash(legacyEvidence, request.EntityHints, request.RelationshipHints)
	require.NoError(t, err)
	repo := &dreamRepositoryStub{getRecord: record}
	remember := &rememberServiceStub{err: errors.New("legacy replay must not call Remember")}
	svc := New(Dependencies{Store: repo, Remember: remember})

	result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", request)
	require.NoError(t, err)
	require.Equal(t, domain.DreamStatusSubmitted, result.Dream.Status)
	require.Equal(t, ingestID, result.Memory.IngestID)
	require.Empty(t, remember.requests)
	require.Empty(t, repo.submitInput)
}

func TestResolveFeedbackRejectsConflictingSubmittedConfirmationBeforeRemember(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	record := repository.HypothesisRecord{
		TeamID:                        teamID.String(),
		HypothesisID:                  hypothesisID,
		CreatedByProfileID:            ownerID.String(),
		Status:                        string(domain.DreamStatusSubmitted),
		Statement:                     "Dense-Mem may use PostgreSQL.",
		SubmittedIngestID:             uuid.NewString(),
		SubmittedIngestIdempotencyKey: "dream-feedback:" + hypothesisID + ":confirm_true",
		SubmittedDecision:             "confirm_true",
	}
	repo := &dreamRepositoryStub{getRecord: record}
	remember := &rememberServiceStub{result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), uuid.NewString())}
	svc := New(Dependencies{Store: repo, Remember: remember})

	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_false",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent refuting evidence."}},
	})
	require.ErrorIs(t, err, ErrDreamNotFound)
	require.Empty(t, remember.requests)
	require.Empty(t, repo.submitInput)
}

func TestResolveFeedbackAddsDreamRetryIdentityToTerminalResubmission(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	attemptID := uuid.NewString()
	repo := &dreamRepositoryStub{getRecord: repository.HypothesisRecord{
		TeamID:             teamID.String(),
		HypothesisID:       hypothesisID,
		CreatedByProfileID: ownerID.String(),
		Status:             string(domain.DreamStatusProposed),
		Statement:          "Dense-Mem may use PostgreSQL.",
	}}
	remember := &rememberServiceStub{result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingRejected), attemptID)}
	remember.result.Terminal.Errors = []rememberapp.SubmissionStatusError{rememberapp.TerminalStatusError(rememberapp.TerminalErrorNoSupportedMemory)}
	svc := New(Dependencies{Store: repo, Remember: remember})

	result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent refuting evidence."}},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Memory)
	require.Equal(t, string(rememberapp.TerminalNextActionRetryDreamFeedback), result.Memory.Terminal.Errors[0].NextAction)
	require.Contains(t, result.Memory.Terminal.Errors[0].Remediation, "resolve_dream_feedback")
	require.Contains(t, result.Memory.Terminal.Errors[0].Remediation, "idempotency_key")
	require.Contains(t, result.Memory.Terminal.Errors[0].Remediation, attemptID)
}

func TestResolveFeedbackUsesCanonicalDreamIDForDefaultRetryKey(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	canonicalID := uuid.NewString()
	aliasID := uuid.NewString()
	ingestID := uuid.NewString()
	repo := &dreamRepositoryStub{getRecord: repository.HypothesisRecord{
		TeamID: teamID.String(), HypothesisID: canonicalID, CreatedByProfileID: ownerID.String(),
		Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
	}}
	remember := &rememberServiceStub{result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), ingestID)}
	svc := New(Dependencies{Store: repo, Remember: remember})

	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: aliasID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	})
	require.NoError(t, err)
	require.Len(t, remember.requests, 1)
	require.Equal(t, "dream-feedback:"+canonicalID+":confirm_true", remember.requests[0].IdempotencyKey)

	remember.result = dreamTerminalRememberResult(string(rememberapp.TerminalProcessingRejected), uuid.NewString())
	remember.result.Terminal.Errors = []rememberapp.SubmissionStatusError{
		rememberapp.TerminalStatusError(rememberapp.TerminalErrorNoSupportedMemory),
	}
	result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: aliasID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	})
	require.NoError(t, err)
	require.Len(t, remember.requests, 2)
	require.Contains(t, remember.requests[1].IdempotencyKey, canonicalID)
	require.Contains(t, result.Memory.Terminal.Errors[0].Remediation, canonicalID)
	require.NotContains(t, result.Memory.Terminal.Errors[0].Remediation, aliasID)
}

func TestResolveFeedbackReplaysAliasWithCanonicalDefaultKey(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	canonicalID := uuid.NewString()
	aliasID := uuid.NewString()
	ingestID := uuid.NewString()
	record := repository.HypothesisRecord{
		TeamID: teamID.String(), HypothesisID: canonicalID, CreatedByProfileID: ownerID.String(),
		Status: string(domain.DreamStatusSubmitted), Statement: "Dense-Mem may use PostgreSQL.",
		SubmittedIngestID: ingestID, SubmittedIngestIdempotencyKey: "dream-feedback:" + canonicalID + ":confirm_true",
		SubmittedDecision: "confirm_true",
	}
	request := ResolveFeedbackRequest{
		DreamID: aliasID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	}
	evidence, err := dreamSubmissionEvidence(request, &record)
	require.NoError(t, err)
	record.SubmittedIngestRequestHash, err = rememberapp.CanonicalRequestBodyHash(evidence, request.EntityHints, request.RelationshipHints)
	require.NoError(t, err)
	repo := &dreamRepositoryStub{getRecord: record}
	remember := &rememberServiceStub{err: errors.New("alias replay must not call Remember")}
	svc := New(Dependencies{Store: repo, Remember: remember})

	result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", request)
	require.NoError(t, err)
	require.Equal(t, domain.DreamStatusSubmitted, result.Dream.Status)
	require.Equal(t, ingestID, result.Memory.IngestID)
	require.Empty(t, remember.requests)
	require.Empty(t, repo.submitInput)
}

func TestResolveFeedbackWrapsConfirmationBusyWithTypedError(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	svc := New(Dependencies{Store: &dreamRepositoryStub{confirmationLockErr: repository.ErrDreamConfirmationBusy}})

	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: uuid.NewString(), Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	})
	var busy *ConfirmationBusyError
	require.ErrorAs(t, err, &busy)
	require.ErrorIs(t, err, repository.ErrDreamConfirmationBusy)
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
