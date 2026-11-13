package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/stretchr/testify/require"
)

func TestRememberProgressFollowsReachedStagesAndReplay(t *testing.T) {
	all := []rememberapp.ProgressStage{rememberapp.ProgressCheckingReplay, rememberapp.ProgressPreparingIntake,
		rememberapp.ProgressPreparingBoundaries, rememberapp.ProgressPartitioning, rememberapp.ProgressValidatingPartitions,
		rememberapp.ProgressDuplicateEmbeddings, rememberapp.ProgressCheckingDuplicates, rememberapp.ProgressAssessingEvidence,
		rememberapp.ProgressValidatingAssessment, rememberapp.ProgressPreparingSearch, rememberapp.ProgressCommitting, rememberapp.ProgressPreparingResult}
	for _, outcome := range []string{"success", "duplicate", "assessment", "embedding", "commit", "replay", "conflict", "security"} {
		t.Run(outcome, func(t *testing.T) {
			input := rememberapp.RememberProcessRequest{TeamID: "team", OwnerProfileID: "owner",
				IdempotencyKey: "key", RequestHash: "hash", Evidence: []rememberapp.EvidenceInput{{Content: "Exact admitted Evidence.", ForceInsert: true}}}
			public := map[string]any{"contract_version": domain.ContractVersion, "submission_id": "original",
				"processing_state": "completed", "search_state": "not_required", "evidence": []any{}, "errors": []any{}}
			ledger := &rememberPipelineLedgerStub{rememberFailureLedgerStub: &rememberFailureLedgerStub{},
				commitResult: &knowledgecontract.SynchronousRememberCommitResult{IngestID: "original", Outcome: "completed", PublicResult: public}}
			provider := &processorAssessmentProviderStub{}
			want := all
			switch outcome {
			case "duplicate":
				ledger.duplicatePlanErr = errors.New("duplicate plan unavailable")
				want = append(append([]rememberapp.ProgressStage{}, all[:6]...), rememberapp.ProgressRecordingFailure)
			case "assessment":
				provider.invalid = true
				want = append(append([]rememberapp.ProgressStage{}, all[:8]...), rememberapp.ProgressValidatingAssessment, rememberapp.ProgressRegeneratingAssessment, rememberapp.ProgressValidatingAssessment, rememberapp.ProgressRegeneratingAssessment, rememberapp.ProgressValidatingAssessment, rememberapp.ProgressRecordingFailure)
			case "embedding":
				ledger.planErr = errors.New("embedding plan unavailable")
				want = append(append([]rememberapp.ProgressStage{}, all[:10]...), rememberapp.ProgressRecordingFailure)
			case "commit":
				ledger.commitErr = errors.New("commit unavailable")
				want = append(append([]rememberapp.ProgressStage{}, all[:11]...), rememberapp.ProgressRecordingFailure)
			case "replay", "conflict":
				hash := "hash"
				if outcome == "conflict" {
					hash = "changed"
				}
				ledger.load = &knowledgecontract.RememberAttempt{AttemptID: "original", RequestHash: hash,
					ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: public}
				want = []rememberapp.ProgressStage{rememberapp.ProgressCheckingReplay}
				if outcome == "replay" {
					want = append(want, rememberapp.ProgressReturningReplay)
				}
			case "security":
				input.SecurityRejected = true
				want = []rememberapp.ProgressStage{rememberapp.ProgressCheckingReplay, rememberapp.ProgressRecordingFailure}
			}
			var reached []rememberapp.ProgressStage
			ctx := rememberapp.WithProgressObserver(t.Context(), func(_ context.Context, stage rememberapp.ProgressStage) {
				reached = append(reached, stage)
				if stage == rememberapp.ProgressCommitting {
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
