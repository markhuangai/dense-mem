package synchronousremember

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	remember "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestSynchronousAssessmentRepairFailurePreservesConsumedTurns(t *testing.T) {
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	ledger := &synchronousPipelineLedger{}
	provider := &synchronousMalformedProvider{repairErr: errors.New("assessor repair unavailable")}
	processor := NewSynchronousRememberProcessor(SynchronousRememberProcessorDependencies{
		Ledger: ledger, Catalog: synchronousPipelineCatalog{}, Provider: provider, Limits: assessor.DefaultSemanticAssessmentLimits(),
		Embeddings: semanticwrite.NewExecutor(&synchronousPipelineEmbeddingProvider{}),
	})

	_, err := processor.ProcessRemember(context.Background(), synchronousPipelineRememberRequest(teamID, ownerID, "pipeline-repair-failure", "pipeline-repair-failure-hash"))

	var processErr *remember.RememberProcessError
	require.ErrorAs(t, err, &processErr)
	require.Equal(t, 1, ledger.failureInput.Attempt.AssessorTurns)
	require.Equal(t, 1, provider.repairs)
}
