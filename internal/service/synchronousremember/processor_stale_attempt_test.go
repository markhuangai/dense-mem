package synchronousremember

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestSynchronousProcessorRecordsStaleResultAsRejectedAttempt(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: &synchronousPipelineProvider{}, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
		BeforeCommit: func(context.Context, remember.RememberProcessRequest, *repository.SynchronousRememberEmbeddingPlan) error {
			return repository.ErrRememberExactReferenceStale
		},
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-stale-rejected", "pipeline-stale-rejected-hash"))

	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, string(remember.TerminalErrorStaleInput), processErr.Result.Errors[0].Code)
	require.Equal(t, 1, ledger.rejectedCalls)
	require.Equal(t, "rejected", ledger.rejectedInput.Outcome)
	require.Equal(t, "commit", ledger.rejectedInput.FailedPhase)
	require.Zero(t, ledger.recordFailureCalls)
}
