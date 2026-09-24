package dream

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestResolveFeedbackUsesExplicitRememberResultKind(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	baseRecord := dreamcontract.HypothesisRecord{
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
			name:       "terminal completed submits committed ingest",
			result:     dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), uuid.NewString()),
			wantStatus: domain.DreamStatusSubmitted,
			wantSubmit: true,
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
			wantError: "terminal Remember result is required",
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

func TestResolveFeedbackRecordsMalformedTerminalResultDiagnostic(t *testing.T) {
	teamID, ownerID := uuid.New(), uuid.New()
	hypothesisID, runID := uuid.NewString(), uuid.NewString()
	diagnostics := &diagnosticRepositoryStub{}
	svc := New(Dependencies{
		Store: &dreamRepositoryStub{getRecord: dreamcontract.HypothesisRecord{
			TeamID: teamID.String(), HypothesisID: hypothesisID, CycleRunID: runID,
			CreatedByProfileID: ownerID.String(), Status: string(domain.DreamStatusProposed),
			Statement: "Dense-Mem may use PostgreSQL.",
		}},
		Remember:    &rememberServiceStub{result: &rememberapp.RememberResult{Kind: rememberapp.ResultKind("future")}},
		Diagnostics: diagnostics,
	})

	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "An independent deployment note."}},
	})

	require.ErrorContains(t, err, "terminal Remember result is required")
	require.Len(t, diagnostics.recorded, 1)
	require.Equal(t, "confirmation", diagnostics.recorded[0].Phase)
	require.Equal(t, "failed", diagnostics.recorded[0].Outcome)
	require.Equal(t, "confirm_true", diagnostics.recorded[0].Details["decision"])
}

func TestResolveFeedbackHonorsCancellationBeforeHypothesisFinalization(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	ctx, cancel := context.WithCancel(dreamTestContext(teamID, ownerID))
	defer cancel()
	repo := &dreamRepositoryStub{
		getRecord: dreamcontract.HypothesisRecord{
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

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	require.Equal(t, 1, repo.submitCalls)

	remember.after = nil
	retried, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	})
	require.NoError(t, err)
	require.NotNil(t, retried)
	require.Equal(t, domain.DreamStatusSubmitted, retried.Dream.Status)
	require.Equal(t, ingestID, retried.Memory.IngestID)
	require.Equal(t, 2, repo.submitCalls)
}

func TestResolveFeedbackRetriesHypothesisFinalizationAfterTransientFailure(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	repo := &dreamRepositoryStub{
		getRecord: dreamcontract.HypothesisRecord{
			TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(),
			Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
		},
		submitErrs: []error{errors.New("temporary database failure"), nil},
	}
	remember := &rememberServiceStub{
		result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), ingestID),
	}
	svc := New(Dependencies{Store: repo, Remember: remember})

	result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
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
	repo := &dreamRepositoryStub{getRecord: dreamcontract.HypothesisRecord{
		TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(), CycleRunID: uuid.NewString(),
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

func TestResolveFeedbackRecordsConfirmationFailureWhenRememberReturnsOrdinaryError(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	repo := &dreamRepositoryStub{getRecord: dreamcontract.HypothesisRecord{
		TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(), CycleRunID: uuid.NewString(),
		Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
	}}
	diagnostics := &diagnosticRepositoryStub{}
	remember := &rememberServiceStub{err: errors.New("database unavailable")}
	svc := New(Dependencies{Store: repo, Remember: remember, Diagnostics: diagnostics})
	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true", IdempotencyKey: "ordinary-error",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent evidence."}},
	})
	require.Error(t, err)
	require.NotEmpty(t, diagnostics.recorded)
	last := diagnostics.recorded[len(diagnostics.recorded)-1]
	require.Equal(t, "confirmation", last.Phase)
	require.Equal(t, "failed", last.Outcome)
	require.Equal(t, "error_present:sha256:0a67cc6110b121fb", last.Cause)
}

func TestResolveFeedbackMarksTruncatedRelationshipResults(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	repo := &dreamRepositoryStub{getRecord: dreamcontract.HypothesisRecord{
		TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(), CycleRunID: uuid.NewString(),
		Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
	}}
	rememberResult := dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), uuid.NewString())
	rememberResult.Terminal.RelationshipResults = []rememberapp.SubmissionRelationshipResult{{
		Disposition: "stored", Splits: make([]rememberapp.SubmissionRelationshipSplit, 9),
	}}
	diagnostics := &diagnosticRepositoryStub{}
	svc := New(Dependencies{Store: repo, Remember: &rememberServiceStub{result: rememberResult}, Diagnostics: diagnostics})

	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true", Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent evidence."}},
	})

	require.NoError(t, err)
	require.Len(t, diagnostics.recorded, 1)
	confirmation := diagnostics.recorded[0]
	require.Equal(t, "confirmation", confirmation.Phase)
	require.Equal(t, true, confirmation.Details["relationship_results_truncated"])
	projected, ok := confirmation.Details["relationship_results"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, projected, 1)
	require.Len(t, projected[0]["splits"], 8)
}

func TestResolveFeedbackLabelsHypothesisFinalizationFailureAsFailed(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	repo := &dreamRepositoryStub{
		getRecord: dreamcontract.HypothesisRecord{
			TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(), CycleRunID: uuid.NewString(),
			Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
		},
		submitErrs: []error{errors.New("submit failed"), errors.New("submit retry failed")},
	}
	diagnostics := &diagnosticRepositoryStub{}
	remember := &rememberServiceStub{result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingCompleted), uuid.NewString())}
	svc := New(Dependencies{Store: repo, Remember: remember, Diagnostics: diagnostics})

	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true", IdempotencyKey: "submit-failure",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent evidence."}},
	})

	require.Error(t, err)
	require.NotEmpty(t, diagnostics.recorded)
	last := diagnostics.recorded[len(diagnostics.recorded)-1]
	require.Equal(t, "confirmation", last.Phase)
	require.Equal(t, "failed", last.Outcome)
}

func TestResolveLifecycleFeedbackLabelsFinalizationFailureAsFailed(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	repo := &dreamRepositoryStub{
		getRecord: dreamcontract.HypothesisRecord{
			TeamID: teamID.String(), HypothesisID: hypothesisID, CreatedByProfileID: ownerID.String(), CycleRunID: uuid.NewString(),
			Status: string(domain.DreamStatusProposed), Statement: "Dense-Mem may use PostgreSQL.",
		},
		updateErr: errors.New("update failed"),
	}
	diagnostics := &diagnosticRepositoryStub{}
	svc := New(Dependencies{Store: repo, Diagnostics: diagnostics})

	_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "reject", Feedback: "no longer useful",
	})

	require.Error(t, err)
	require.NotEmpty(t, diagnostics.recorded)
	last := diagnostics.recorded[len(diagnostics.recorded)-1]
	require.Equal(t, "feedback", last.Phase)
	require.Equal(t, "failed", last.Outcome)
}

func TestResolveFeedbackReplaysPreUpgradeSubmittedRememberWithoutCallingRemember(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	record := dreamcontract.HypothesisRecord{
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
	record.SubmittedIngestRequestHash, err = rememberapp.CanonicalLegacyRequestBodyHash(legacyEvidence, request.EntityHints, request.RelationshipHints)
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

func TestResolveFeedbackReplaysV261SubmittedRememberWithoutCallingRemember(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	record := dreamcontract.HypothesisRecord{
		TeamID:                        teamID.String(),
		HypothesisID:                  hypothesisID,
		CreatedByProfileID:            ownerID.String(),
		Status:                        string(domain.DreamStatusSubmitted),
		Statement:                     "Dense-Mem may use PostgreSQL.",
		InvalidatedReason:             "v2.6.1 confirmation reason",
		SubmittedIngestID:             ingestID,
		SubmittedIngestIdempotencyKey: "v261-dream-replay",
		SubmittedDecision:             "confirm_true",
	}
	request := ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true", Feedback: record.InvalidatedReason,
		IdempotencyKey: record.SubmittedIngestIdempotencyKey,
		Evidence:       []rememberapp.RememberEvidenceInput{{Content: "Independent v2.6.1 deployment evidence."}},
	}
	evidence, err := dreamSubmissionEvidence(request, &record)
	require.NoError(t, err)
	record.SubmittedIngestRequestHash, err = rememberapp.CanonicalLegacyRequestBodyHash(evidence, request.EntityHints, request.RelationshipHints)
	require.NoError(t, err)
	repo := &dreamRepositoryStub{getRecord: record}
	remember := &rememberServiceStub{err: errors.New("v2.6.1 replay must not call Remember")}
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
	record := dreamcontract.HypothesisRecord{
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

func TestResolveFeedbackPreservesNonRetryablePolicyGuidance(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	attemptID := uuid.NewString()
	repo := &dreamRepositoryStub{getRecord: dreamcontract.HypothesisRecord{
		TeamID:             teamID.String(),
		HypothesisID:       hypothesisID,
		CreatedByProfileID: ownerID.String(),
		Status:             string(domain.DreamStatusProposed),
		Statement:          "Dense-Mem may use PostgreSQL.",
	}}
	remember := &rememberServiceStub{result: dreamTerminalRememberResult(string(rememberapp.TerminalProcessingFailed), attemptID)}
	remember.result.Terminal.Errors = []rememberapp.SubmissionStatusError{rememberapp.TerminalStatusError(rememberapp.TerminalErrorPolicyRejected)}
	metrics := observability.NewPrometheusMetrics()
	svc := New(Dependencies{Store: repo, Remember: remember, Metrics: metrics})

	result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: hypothesisID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent refuting evidence."}},
	})
	require.NoError(t, err)
	require.NotNil(t, result.Memory)
	require.Equal(t, string(rememberapp.TerminalNextActionResubmitRemember), result.Memory.Terminal.Errors[0].NextAction)
	require.NotContains(t, result.Memory.Terminal.Errors[0].Remediation, "resolve_dream_feedback")
	require.NotContains(t, result.Memory.Terminal.Errors[0].Remediation, attemptID)
	metricText := dreamMetricsText(t, metrics)
	require.Contains(t, metricText, `densemem_logical_operation_attempts_total{classification="confirmation",operation="dream_confirmation",outcome="failed"} 1`)
	require.Contains(t, metricText, `densemem_logical_operation_attempts_total{classification="confirmation",operation="dream_confirmation",outcome="completed"} 0`)
}

func TestResolveFeedbackUsesCanonicalDreamIDForDefaultRetryKey(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	canonicalID := uuid.NewString()
	aliasID := uuid.NewString()
	ingestID := uuid.NewString()
	repo := &dreamRepositoryStub{getRecord: dreamcontract.HypothesisRecord{
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

	remember.result = dreamTerminalRememberResult(string(rememberapp.TerminalProcessingFailed), uuid.NewString())
	remember.result.Terminal.Errors = []rememberapp.SubmissionStatusError{
		rememberapp.TerminalStatusError(rememberapp.TerminalErrorPolicyRejected),
	}
	result, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
		DreamID: aliasID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	})
	require.NoError(t, err)
	require.Len(t, remember.requests, 2)
	require.Contains(t, remember.requests[1].IdempotencyKey, canonicalID)
	require.Equal(t, string(rememberapp.TerminalNextActionResubmitRemember), result.Memory.Terminal.Errors[0].NextAction)
	require.NotContains(t, result.Memory.Terminal.Errors[0].Remediation, canonicalID)
	require.NotContains(t, result.Memory.Terminal.Errors[0].Remediation, aliasID)
}

func TestResolveFeedbackReplaysAliasWithCanonicalDefaultKey(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	canonicalID := uuid.NewString()
	aliasID := uuid.NewString()
	ingestID := uuid.NewString()
	record := dreamcontract.HypothesisRecord{
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

func TestResolveFeedbackReplaysAliasWithLegacyDefaultKey(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	canonicalID := uuid.NewString()
	aliasID := uuid.NewString()
	ingestID := uuid.NewString()
	record := dreamcontract.HypothesisRecord{
		TeamID: teamID.String(), HypothesisID: canonicalID, CreatedByProfileID: ownerID.String(),
		Status: string(domain.DreamStatusSubmitted), Statement: "Dense-Mem may use PostgreSQL.",
		SubmittedIngestID: ingestID, SubmittedIngestIdempotencyKey: "dream-feedback:" + aliasID + ":confirm_true",
		SubmittedDecision: "confirm_true",
	}
	request := ResolveFeedbackRequest{
		DreamID: aliasID, Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
	}
	legacyEvidence, err := dreamSubmissionEvidenceWithStatus(request, &record, string(domain.DreamStatusProposed), true)
	require.NoError(t, err)
	record.SubmittedIngestRequestHash, err = rememberapp.CanonicalLegacyRequestBodyHash(legacyEvidence, request.EntityHints, request.RelationshipHints)
	require.NoError(t, err)
	repo := &dreamRepositoryStub{getRecord: record}
	remember := &rememberServiceStub{err: errors.New("legacy alias replay must not call Remember")}
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
	for _, decision := range []string{"confirm_true", "reject"} {
		t.Run(decision, func(t *testing.T) {
			hypothesisID := uuid.NewString()
			metrics := observability.NewInMemoryDiscoverabilityMetrics()
			svc := New(Dependencies{
				Store: &dreamRepositoryStub{
					getRecord: dreamcontract.HypothesisRecord{
						TeamID:             teamID.String(),
						HypothesisID:       hypothesisID,
						CreatedByProfileID: ownerID.String(),
						Status:             string(domain.DreamStatusProposed),
						Statement:          "Dense-Mem may use PostgreSQL.",
					},
					confirmationLockErr: dreamcontract.ErrDreamConfirmationBusy,
				},
				Metrics: metrics,
			})

			_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
				DreamID:  hypothesisID,
				Decision: decision,
				Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
			})
			var busy *ConfirmationBusyError
			require.ErrorAs(t, err, &busy)
			require.ErrorIs(t, err, dreamcontract.ErrDreamConfirmationBusy)

			samples := metrics.DreamFeedbackSamples()
			require.Len(t, samples, 1)
			require.Equal(t, decision, samples[0].Decision)
			require.Equal(t, "error", samples[0].Outcome)
		})
	}
}

func TestResolveFeedbackRecordsNonBusyLockAcquisitionErrors(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	for _, decision := range []string{"confirm_true", "reject"} {
		t.Run(decision, func(t *testing.T) {
			hypothesisID := uuid.NewString()
			lockErr := errors.New("confirmation database unavailable")
			metrics := observability.NewInMemoryDiscoverabilityMetrics()
			svc := New(Dependencies{
				Store: &dreamRepositoryStub{
					getRecord: dreamcontract.HypothesisRecord{
						TeamID:             teamID.String(),
						HypothesisID:       hypothesisID,
						CreatedByProfileID: ownerID.String(),
						Status:             string(domain.DreamStatusProposed),
						Statement:          "Dense-Mem may use PostgreSQL.",
					},
					confirmationLockErr: lockErr,
				},
				Metrics: metrics,
			})

			_, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", ResolveFeedbackRequest{
				DreamID:  hypothesisID,
				Decision: decision,
				Evidence: []rememberapp.RememberEvidenceInput{{Content: "Independent deployment evidence."}},
			})
			require.ErrorIs(t, err, lockErr)
			var busy *ConfirmationBusyError
			require.False(t, errors.As(err, &busy))

			samples := metrics.DreamFeedbackSamples()
			require.Len(t, samples, 1)
			require.Equal(t, decision, samples[0].Decision)
			require.Equal(t, "error", samples[0].Outcome)
		})
	}
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
