package service

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/stretchr/testify/require"
)

func TestRememberProgressFollowsReachedStagesAndReplay(t *testing.T) {
	all := []ProgressStage{ProgressCheckingReplay, ProgressPreparingIntake,
		ProgressPreparingBoundaries, ProgressPartitioning, ProgressValidatingPartitions,
		ProgressDuplicateEmbeddings, ProgressCheckingDuplicates, ProgressAssessingEvidence,
		ProgressValidatingAssessment, ProgressPreparingSearch, ProgressCommitting, ProgressPreparingResult}
	for _, outcome := range []string{"success", "duplicate", "assessment", "embedding", "commit", "replay", "conflict", "security"} {
		t.Run(outcome, func(t *testing.T) {
			input := RememberProcessRequest{TeamID: "team", OwnerProfileID: "owner",
				IdempotencyKey: "key", RequestHash: "hash", Evidence: []EvidenceInput{{Content: "Exact admitted Evidence.", ForceInsert: true}}}
			public := map[string]any{"contract_version": domain.ContractVersion, "submission_id": "original",
				"processing_state": "completed", "search_state": "not_required", "evidence": []any{}, "errors": []any{}}
			ledger := &rememberPipelineLedgerStub{rememberFailureLedgerStub: &rememberFailureLedgerStub{},
				commitResult: &knowledgecontract.SynchronousRememberCommitResult{IngestID: "original", Outcome: "completed", Receipt: mustTestRememberReceipt(t, public)}}
			provider := &processorAssessmentProviderStub{}
			want := all
			switch outcome {
			case "duplicate":
				ledger.duplicatePlanErr = errors.New("duplicate plan unavailable")
				want = append(append([]ProgressStage{}, all[:6]...), ProgressRecordingFailure)
			case "assessment":
				provider.invalid = true
				want = append(append([]ProgressStage{}, all[:8]...), ProgressValidatingAssessment, ProgressRegeneratingAssessment, ProgressValidatingAssessment, ProgressRegeneratingAssessment, ProgressValidatingAssessment, ProgressRecordingFailure)
			case "embedding":
				ledger.planErr = errors.New("embedding plan unavailable")
				want = append(append([]ProgressStage{}, all[:10]...), ProgressRecordingFailure)
			case "commit":
				ledger.commitErr = errors.New("commit unavailable")
				want = append(append([]ProgressStage{}, all[:11]...), ProgressRecordingFailure)
			case "replay", "conflict":
				hash := "hash"
				if outcome == "conflict" {
					hash = "changed"
				}
				ledger.load = &knowledgecontract.RememberAttempt{AttemptID: "original", RequestHash: hash,
					ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: public}
				want = []ProgressStage{ProgressCheckingReplay}
				if outcome == "replay" {
					want = append(want, ProgressReturningReplay)
				}
			case "security":
				input.SecurityRejected = true
				want = []ProgressStage{ProgressCheckingReplay, ProgressRecordingFailure}
			}
			var reached []ProgressStage
			ctx := WithProgressObserver(t.Context(), func(_ context.Context, stage ProgressStage) {
				reached = append(reached, stage)
				if stage == ProgressCommitting {
					require.Empty(t, ledger.commitInput.TeamID, "notification must precede the semantic transaction")
				}
			})
			_, err := (&rememberSynchronousProcessor{ledger: ledger, provider: provider}).ProcessRemember(ctx, input)
			if outcome == "success" || outcome == "replay" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, want, reached)
			if outcome == "replay" || outcome == "conflict" || outcome == "security" || outcome == "duplicate" {
				require.Zero(t, provider.assessCalls)
			}
			if outcome == "assessment" {
				require.Positive(t, provider.repairCalls)
			}
		})
	}
}
