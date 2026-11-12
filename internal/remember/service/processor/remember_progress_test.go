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
	all := []rememberapp.ProgressStage{rememberapp.ProgressPreparingIntake, rememberapp.ProgressCheckingDuplicates,
		rememberapp.ProgressAssessingEvidence, rememberapp.ProgressPreparingSearch, rememberapp.ProgressCommitting}
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
				want = all[:2]
			case "assessment":
				provider.invalid = true
				want = all[:3]
			case "embedding":
				ledger.planErr = errors.New("embedding plan unavailable")
				want = all[:4]
			case "commit":
				ledger.commitErr = errors.New("commit unavailable")
			case "replay", "conflict":
				hash := "hash"
				if outcome == "conflict" {
					hash = "changed"
				}
				ledger.load = &knowledgecontract.RememberAttempt{AttemptID: "original", RequestHash: hash,
					ContractVersion: domain.ContractVersion, Outcome: "completed", PublicResult: public}
				want = nil
			case "security":
				input.SecurityRejected = true
				want = nil
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
