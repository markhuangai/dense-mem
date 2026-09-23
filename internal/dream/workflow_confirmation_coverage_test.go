package dream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestDreamConfirmationHelperBranches(t *testing.T) {
	profileID := uuid.New()
	record := &dreamcontract.HypothesisRecord{
		HypothesisID: "hypothesis",
		Status:       string(domain.DreamStatusSubmitted),
		Statement:    "the hypothesis",
		SourceOwnerProfileIDs: []string{
			profileID.String(), "not-a-uuid",
		},
	}
	require.False(t, dreamEvidenceHypothesisOwnedBy(nil, profileID.String()))
	require.False(t, dreamEvidenceHypothesisOwnedBy(record, "not-a-uuid"))
	require.True(t, dreamEvidenceHypothesisOwnedBy(record, profileID.String()))
	require.False(t, dreamEvidenceHypothesisOwnedBy(record, uuid.NewString()))

	for _, decision := range []string{"reject", "stale", "reinforce", "unknown"} {
		_ = lifecycleStatus(decision)
	}
	require.Equal(t, string(domain.DreamStatusRejected), lifecycleStatus("reject"))
	require.Equal(t, "", lifecycleStatus("unknown"))

	result := &rememberapp.RememberResult{Terminal: &rememberapp.TerminalRememberResult{
		SubmissionID: "submission",
		Errors: []rememberapp.SubmissionStatusError{
			{Retryable: true, NextAction: string(rememberapp.TerminalNextActionResubmitRemember)},
			{Retryable: false, NextAction: string(rememberapp.TerminalNextActionResubmitRemember)},
			{Retryable: true, NextAction: string(rememberapp.TerminalNextActionNone)},
		},
	}}
	applyDreamTerminalRetryGuidance(nil, "dream", "confirm_true")
	applyDreamTerminalRetryGuidance(&rememberapp.RememberResult{}, "dream", "confirm_true")
	applyDreamTerminalRetryGuidance(result, strings.Repeat("d", 100), strings.Repeat("c", 40))
	require.Equal(t, string(rememberapp.TerminalNextActionRetryDreamFeedback), result.Terminal.Errors[0].NextAction)
	require.NotEmpty(t, result.Terminal.Errors[0].Remediation)
	require.Equal(t, string(rememberapp.TerminalNextActionResubmitRemember), result.Terminal.Errors[1].NextAction)

	require.Nil(t, dreamReplayRememberResult(nil))
	replay := dreamReplayRememberResult(&dreamcontract.HypothesisRecord{SubmittedIngestID: "ingest"})
	require.Equal(t, "ingest", replay.SubmissionID)
	require.Nil(t, rememberResultFromProcessError(nil))
	terminal := &rememberapp.TerminalRememberResult{SubmissionID: "submission", SubmissionKind: "remember", ProcessingState: string(rememberapp.TerminalProcessingFailed)}
	fromProcess := rememberResultFromProcessError(terminal)
	require.Equal(t, "submission", fromProcess.SubmissionID)

	for name, value := range map[string]*rememberapp.RememberResult{
		"nil":         nil,
		"nonterminal": {},
		"no terminal": {Kind: rememberapp.ResultKindTerminal},
		"unsupported": {Kind: rememberapp.ResultKindTerminal, Terminal: &rememberapp.TerminalRememberResult{ProcessingState: "unknown"}},
		"bad ingest":  {Kind: rememberapp.ResultKindTerminal, Terminal: &rememberapp.TerminalRememberResult{ProcessingState: string(rememberapp.TerminalProcessingCompleted), SubmissionID: "not-uuid"}},
	} {
		t.Run(name, func(t *testing.T) {
			if completed, ingest, err := dreamRememberCompletion(value); err == nil || completed || ingest != "" {
				t.Fatalf("completion = %v/%q/%v", completed, ingest, err)
			}
		})
	}
	completedID := uuid.NewString()
	completed, ingest, err := dreamRememberCompletion(&rememberapp.RememberResult{Kind: rememberapp.ResultKindTerminal, Terminal: &rememberapp.TerminalRememberResult{ProcessingState: string(rememberapp.TerminalProcessingCompleted), SubmissionID: completedID}})
	require.NoError(t, err)
	require.True(t, completed)
	require.Equal(t, completedID, ingest)
	failed, ingest, err := dreamRememberCompletion(&rememberapp.RememberResult{Kind: rememberapp.ResultKindTerminal, Terminal: &rememberapp.TerminalRememberResult{ProcessingState: string(rememberapp.TerminalProcessingFailed)}})
	require.NoError(t, err)
	require.False(t, failed)
	require.Empty(t, ingest)

	keySvc := &service{}
	if _, err := keySvc.confirmationIdempotencyKey(context.Background(), "team", "profile", ResolveFeedbackRequest{}, nil, "confirm_true"); err == nil {
		t.Fatal("nil hypothesis record was accepted for idempotency")
	}
	key, err := keySvc.confirmationIdempotencyKey(context.Background(), "team", "profile", ResolveFeedbackRequest{IdempotencyKey: " client-key "}, record, "confirm_true")
	require.NoError(t, err)
	require.Equal(t, "client-key", key)
}

func TestDreamConfirmationReplayMatchingBranches(t *testing.T) {
	req := ResolveFeedbackRequest{DreamID: "dream", Decision: "confirm_true", Feedback: "reason", Evidence: []rememberapp.RememberEvidenceInput{{Content: "independent evidence"}}}
	record := &dreamcontract.HypothesisRecord{HypothesisID: "dream", Status: string(domain.DreamStatusProposed)}
	require.True(t, dreamConfirmationReplayMatches(record, req, req.Decision))
	record.Status = string(domain.DreamStatusSubmitted)
	record.SubmittedIngestID = "ingest"
	require.False(t, dreamConfirmationReplayMatches(record, req, req.Decision))
	record.SubmittedIngestIdempotencyKey = dreamDefaultFeedbackIdempotency("dream", req.Decision)
	record.SubmittedDecision = req.Decision
	require.True(t, dreamConfirmationReplayMatches(record, req, req.Decision))
	require.False(t, dreamConfirmationReplayMatches(record, req, "confirm_false"))
}

func TestDeferredHypothesisDiagnosticPersistsAfterCallerCancellation(t *testing.T) {
	teamID, runID, hypothesisID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	diagnostics := &diagnosticRepositoryStub{rejectCanceled: true}
	deferred := deferredHypothesisDiagnostic{}
	deferred.capture(&dreamcontract.HypothesisRecord{
		TeamID: teamID, CycleRunID: runID, HypothesisID: hypothesisID,
	}, "confirmation", "failed", "terminal result rejected", map[string]any{"decision": "confirm_true"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deferred.recordAfterLock(&service{deps: Dependencies{Diagnostics: diagnostics}}, ctx)

	require.Len(t, diagnostics.recorded, 1)
	require.NoError(t, diagnostics.phaseContextErr)
	require.Equal(t, "confirmation", diagnostics.recorded[0].Phase)
}

func TestResolveFeedbackErrorBranches(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	record := dreamcontract.HypothesisRecord{
		TeamID:             teamID.String(),
		HypothesisID:       hypothesisID,
		CreatedByProfileID: ownerID.String(),
		Status:             string(domain.DreamStatusProposed),
		Statement:          "Dense-Mem may use PostgreSQL.",
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	ctx := dreamTestContext(teamID, ownerID)

	svc := New(Dependencies{
		Store:     &dreamRepositoryStub{getErr: dreamcontract.ErrDreamHypothesisNotFound},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err := svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject"})
	require.ErrorIs(t, err, ErrDreamNotFound)

	svc = New(Dependencies{
		Store: &dreamRepositoryStub{
			getRecord:           record,
			confirmationLockErr: dreamcontract.ErrDreamConfirmationBusy,
		},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject"})
	var busyErr *ConfirmationBusyError
	require.ErrorAs(t, err, &busyErr)

	svc = New(Dependencies{
		Store:     &dreamRepositoryStub{getRecord: record},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Evidence: []rememberapp.RememberEvidenceInput{{
			Content: "The deployment note says Dense-Mem uses PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "remember service is required")

	svc = New(Dependencies{
		Store: &dreamRepositoryStub{
			getRecord: record,
			updateErr: dreamcontract.ErrDreamHypothesisNotFound,
		},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject"})
	require.ErrorIs(t, err, ErrDreamNotFound)

	svc = New(Dependencies{
		Store:     &dreamRepositoryStub{getRecord: record},
		Remember:  &rememberServiceStub{err: errors.New("remember failed")},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_false",
		Evidence: []rememberapp.RememberEvidenceInput{{
			Content: "The deployment note says Dense-Mem does not use PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "remember failed")
}
