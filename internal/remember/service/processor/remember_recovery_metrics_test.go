package processor

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestRememberRetryRecordsRecoveryAndOneLogicalCompletion(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "retry-key", RequestHash: "retry-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "fact", ForceInsert: true}},
	}
	commitResult := map[string]any{
		"contract_version": domain.ContractVersion, "submission_id": "retried", "submission_kind": "remember",
		"processing_state": "completed", "search_state": "current", "evidence": []any{}, "relationship_results": []any{}, "errors": []any{},
	}
	ledger := &rememberPipelineLedgerStub{
		rememberFailureLedgerStub: &rememberFailureLedgerStub{load: &knowledgecontract.RememberAttempt{
			AttemptID: "failed-attempt", RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion,
			Outcome: "failed", Retryable: true,
		}},
		plan: &knowledgecontract.InlineEmbeddingPlan{},
		commitResult: &knowledgecontract.SynchronousRememberCommitResult{
			IngestID: "retried", Outcome: "completed", PublicResult: commitResult,
		},
	}
	processor := &rememberSynchronousProcessor{
		ledger: ledger, catalog: &processorAssessmentCatalogStub{},
		provider: &processorAssessmentProviderStub{}, metrics: metrics,
	}
	status, err := processor.ProcessRemember(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "retried", status.SubmissionID)
	require.Equal(t, "execution", ledger.invocation.Classification)
	metricText := rememberMetricsText(t, metrics)
	require.Contains(t, metricText, `densemem_logical_operation_recoveries_total{operation="remember",outcome="attempted"} 1`)
	require.Contains(t, metricText, `densemem_logical_operation_recoveries_total{operation="remember",outcome="succeeded"} 1`)
	require.Contains(t, metricText, `densemem_logical_operation_attempts_total{classification="recovery",operation="remember",outcome="evaluated_zero"} 1`)
}

func TestCanceledBeforeCommitRecordsCommitPhaseOutcome(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	input := rememberapp.RememberProcessRequest{
		TeamID: "team", OwnerProfileID: "owner", IdempotencyKey: "cancel-key", RequestHash: "cancel-hash",
		Evidence: []rememberapp.EvidenceInput{{Content: "fact", ForceInsert: true}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	ledger := &cancelBeforeCommitLedger{
		rememberPipelineLedgerStub: &rememberPipelineLedgerStub{
			rememberFailureLedgerStub: &rememberFailureLedgerStub{},
			plan:                      &knowledgecontract.InlineEmbeddingPlan{},
		},
		cancel: cancel,
	}
	processor := &rememberSynchronousProcessor{
		ledger: ledger, catalog: &processorAssessmentCatalogStub{},
		provider: &processorAssessmentProviderStub{}, metrics: metrics,
	}

	_, err := processor.ProcessRemember(ctx, input)
	require.Error(t, err)
	require.Zero(t, ledger.commitCalls)
	metricText := rememberMetricsText(t, metrics)
	require.True(t, strings.Contains(metricText, `densemem_remember_phase_duration_seconds_count{outcome="cancelled",phase="commit"} 1`), "cancelled commit phase sample is missing")
}

type cancelBeforeCommitLedger struct {
	*rememberPipelineLedgerStub
	cancel      context.CancelFunc
	commitCalls int
}

func (l *cancelBeforeCommitLedger) PlanRememberEmbeddings(ctx context.Context, input knowledgecontract.SynchronousRememberCommitInput) (*knowledgecontract.InlineEmbeddingPlan, error) {
	plan, err := l.rememberPipelineLedgerStub.PlanRememberEmbeddings(ctx, input)
	l.cancel()
	return plan, err
}

func (l *cancelBeforeCommitLedger) CommitRememberWithEmbeddings(ctx context.Context, input knowledgecontract.SynchronousRememberCommitInput, results []knowledgecontract.InlineEmbeddingResult) (*knowledgecontract.SynchronousRememberCommitResult, error) {
	l.commitCalls++
	return l.rememberPipelineLedgerStub.CommitRememberWithEmbeddings(ctx, input, results)
}
