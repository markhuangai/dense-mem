package dream

import (
	"context"
	"strings"
	"testing"

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
