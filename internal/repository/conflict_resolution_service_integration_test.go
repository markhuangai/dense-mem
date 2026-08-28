package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/repository"
	conflictreviewservice "github.com/markhuangai/dense-mem/internal/service/conflictreview"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestDeterministicConflictEmbeddingFailureReleasesClaimAndRetries(t *testing.T) {
	fixture := repository.NewDeterministicConflictServiceFixture(t)

	embedder := &deterministicConflictEmbeddingProvider{failed: true}
	reviewer, err := conflictreviewservice.New(conflictreviewservice.Dependencies{
		Repository: fixture.Ledger, Provider: deterministicConflictProvider{}, Embeddings: embedder,
		EmbeddingTimeout: time.Second, Timezone: "UTC", Limits: conflictassessment.DefaultSemanticAssessmentLimits(),
	})
	require.NoError(t, err)
	before := fixture.Snapshot(t)
	failedResult, err := reviewer.ReviewRelationshipConflictCase(context.Background(), repository.ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID, ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
	})
	require.ErrorIs(t, err, semanticwrite.ErrProviderUnavailable)
	assert.Nil(t, failedResult)
	after := fixture.Snapshot(t)
	assert.Equal(t, before.CaseState, after.CaseState)
	assert.Equal(t, before.RelationshipState, after.RelationshipState)
	assert.Equal(t, before.SearchDocumentState, after.SearchDocumentState)
	assert.Equal(t, before.EmbeddingJobState, after.EmbeddingJobState)
	assert.Equal(t, 1, before.CaseAttempts)
	assert.Equal(t, 0, after.CaseAttempts)
	assert.Empty(t, after.CaseLeaseWorkerID)
	assert.Nil(t, after.CaseLeaseUntil)

	embedder.failed = false
	reclaimed, err := fixture.Ledger.ClaimRelationshipConflictCases(context.Background(), repository.ClaimRelationshipConflictCasesInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		Limit: 1, Lease: time.Minute, MaxAttempts: 5, Now: fixture.ReviewNow.Add(time.Second),
	})
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	resolved, err := reviewer.ReviewRelationshipConflictCase(context.Background(), repository.ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		ConflictID: fixture.ConflictID, Now: fixture.ReviewNow.Add(time.Second),
	})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, repository.ConflictReviewOutcomeResolve, resolved.Outcome)
	assert.Equal(t, repository.ConflictReviewStageDueMajority, resolved.Stage)
}

type deterministicConflictProvider struct{}

func (deterministicConflictProvider) AssessRelationshipConflict(context.Context, conflictassessment.ConflictAssessmentRequest) (conflictassessment.ConflictAssessmentResponse, error) {
	return conflictassessment.ConflictAssessmentResponse{}, errors.New("deterministic conflict provider must not be called")
}

func (deterministicConflictProvider) ModelName() string { return "test-model" }

type deterministicConflictEmbeddingProvider struct {
	failed bool
}

func (p *deterministicConflictEmbeddingProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, string, error) {
	if p.failed {
		return nil, "test-model", errors.New("forced deterministic embedding failure")
	}
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0}
	}
	return vectors, "test-model", nil
}

func (*deterministicConflictEmbeddingProvider) ModelName() string { return "test-model" }
func (*deterministicConflictEmbeddingProvider) Dimensions() int   { return 3 }
func (*deterministicConflictEmbeddingProvider) IsAvailable() bool { return true }
