package embeddingservice

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestEmbeddingReconciliationTimingUsesConfiguredProviderTimeout(t *testing.T) {
	callTimeout, lease := embeddingReconciliationTiming(0)
	assert.Equal(t, reconciliationCallTimeout, callTimeout)
	assert.Equal(t, reconciliationLease, lease)

	callTimeout, lease = embeddingReconciliationTiming(3 * time.Minute)
	assert.Equal(t, 3*time.Minute, callTimeout)
	assert.Equal(t, reconciliationLease, lease)

	callTimeout, lease = embeddingReconciliationTiming(12 * time.Minute)
	assert.Equal(t, 12*time.Minute, callTimeout)
	assert.Equal(t, 12*time.Minute+reconciliationCleanupTimeout, lease)
}

func TestEmbeddingReconciliationLeavesPreCanaryRunReclaimableOnCancellation(t *testing.T) {
	for _, test := range []struct {
		name           string
		reconciliation *reconciliationRepositoryStub
	}{
		{
			name:           "selection",
			reconciliation: &reconciliationRepositoryStub{selectErr: context.Canceled},
		},
		{
			name: "marker",
			reconciliation: &reconciliationRepositoryStub{
				job:     &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", EmbeddingDimensions: 3, DocumentText: "canary"},
				markErr: context.Canceled,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			search := newEmbeddingSearchStub()
			search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
			provider := &reconciliationRawProvider{available: true, model: "model", dims: 3, vector: []float32{1, 0, 0}}
			now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
			service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
				Search: search, Reconciliation: test.reconciliation, Provider: provider,
				AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
				WorkerID:  "worker-1", Now: func() time.Time { return now },
			})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, err := service.ProcessDue(ctx)

			require.ErrorIs(t, err, context.Canceled)
			assert.Nil(t, test.reconciliation.completed, "the unattempted run must remain lease-reclaimable")
			assert.Zero(t, provider.calls)
		})
	}
}

func TestEmbeddingReconciliationPreservesFailedCanaryWhenTeamBecomesInactive(t *testing.T) {
	search := newEmbeddingSearchStub()
	search.contract = repository.ActiveSearchContract{EmbeddingContractID: "contract-1", EmbeddingDimensions: 3, EmbeddingModel: "model"}
	search.failErr = repository.ErrTeamInactive
	reconciliation := &reconciliationRepositoryStub{
		job: &repository.EmbeddingJob{TeamID: "team-1", EmbeddingJobID: "job-1", EmbeddingDimensions: 3, DocumentText: "canary"},
	}
	provider := &reconciliationRawProvider{
		available: true, model: "model", dims: 3,
		err: &embedding.ProviderHTTPError{Status: 429, Code: "insufficient_quota"},
	}
	now := time.Date(2026, 8, 10, 4, 30, 20, 0, time.UTC)
	service := NewEmbeddingReconciliationService(EmbeddingReconciliationDependencies{
		Search: search, Reconciliation: reconciliation, Provider: provider,
		AppConfig: reconciliationConfigStub{runtime: domain.GeneralRuntimeConfig{Timezone: "UTC", EmbeddingReconciliationStartTimeLocal: "04:30"}},
		WorkerID:  "worker-1", Now: func() time.Time { return now },
	})

	_, err := service.ProcessDue(context.Background())

	require.NoError(t, err)
	require.NotNil(t, reconciliation.canaryInput)
	assert.False(t, reconciliation.canaryInput.Succeeded)
	assert.Equal(t, string(domain.EmbeddingFailureProviderQuotaExhausted), reconciliation.canaryInput.FailureCode)
	require.NotNil(t, reconciliation.completed)
	assert.Equal(t, string(domain.EmbeddingReconciliationDeferred), reconciliation.completed.Status)
	assert.Equal(t, "failed", reconciliation.completed.CanaryOutcome)
	assert.Equal(t, string(domain.EmbeddingFailureProviderQuotaExhausted), reconciliation.completed.FailureCode)
}
