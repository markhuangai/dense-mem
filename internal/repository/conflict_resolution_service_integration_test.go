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

func TestDeterministicConflictEmbeddingTimeoutReleasesClaimAndRetries(t *testing.T) {
	fixture := repository.NewDeterministicConflictServiceFixture(t)

	embedder := &deterministicConflictEmbeddingProvider{timeout: true}
	reviewer, err := conflictreviewservice.New(conflictreviewservice.Dependencies{
		Repository: fixture.Ledger, Provider: deterministicConflictProvider{}, Embeddings: embedder,
		EmbeddingTimeout: 25 * time.Millisecond, Timezone: "UTC", Limits: conflictassessment.DefaultSemanticAssessmentLimits(),
	})
	require.NoError(t, err)
	before := fixture.Snapshot(t)
	failedResult, err := reviewer.ReviewRelationshipConflictCase(context.Background(), repository.ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID, ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
	})
	require.ErrorIs(t, err, semanticwrite.ErrProviderTimeout)
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

	embedder.timeout = false
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

func TestDeterministicConflictResolutionReleasesExpiredClaimAfterEmbedding(t *testing.T) {
	fixture := repository.NewDeterministicConflictServiceFixture(t)

	embedder := &blockingDeterministicConflictEmbeddingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	reviewer, err := conflictreviewservice.New(conflictreviewservice.Dependencies{
		Repository: fixture.Ledger, Provider: deterministicConflictProvider{}, Embeddings: embedder,
		EmbeddingTimeout: 5 * time.Second, Timezone: "UTC", Limits: conflictassessment.DefaultSemanticAssessmentLimits(),
	})
	require.NoError(t, err)
	before := fixture.Snapshot(t)
	type reviewResult struct {
		result *repository.ReviewRelationshipConflictCaseResult
		err    error
	}
	completed := make(chan reviewResult, 1)
	go func() {
		result, reviewErr := reviewer.ReviewRelationshipConflictCase(context.Background(), repository.ReviewRelationshipConflictCaseInput{
			TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
			ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
		})
		completed <- reviewResult{result: result, err: reviewErr}
	}()
	<-embedder.started
	fixture.ExpireClaim(t)
	close(embedder.release)
	reviewed := <-completed
	require.NoError(t, reviewed.err)
	require.NotNil(t, reviewed.result)
	assert.Equal(t, repository.ConflictReviewOutcomeNoop, reviewed.result.Outcome)
	assert.Equal(t, "resolution_stale", reviewed.result.Stage)

	after := fixture.Snapshot(t)
	assert.Equal(t, before.CaseState, after.CaseState)
	assert.Equal(t, before.RelationshipState, after.RelationshipState)
	assert.Equal(t, before.SearchDocumentState, after.SearchDocumentState)
	assert.Equal(t, before.EmbeddingJobState, after.EmbeddingJobState)
	assert.Equal(t, 1, before.CaseAttempts)
	assert.Equal(t, 0, after.CaseAttempts)
	assert.Empty(t, after.CaseLeaseWorkerID)
	assert.Nil(t, after.CaseLeaseUntil)

	reclaimed, err := fixture.Ledger.ClaimRelationshipConflictCases(context.Background(), repository.ClaimRelationshipConflictCasesInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		Limit: 1, Lease: time.Minute, MaxAttempts: 1, Now: fixture.ReviewNow.Add(time.Second),
	})
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
}

func TestOversizedDeterministicConflictDefersBeforeEmbeddingOrSemanticWrites(t *testing.T) {
	fixture := repository.NewOversizedConflictServiceFixture(t)
	embedder := &deterministicConflictEmbeddingProvider{}
	reviewer, err := conflictreviewservice.New(conflictreviewservice.Dependencies{
		Repository: fixture.Ledger, Provider: deterministicConflictProvider{}, Embeddings: embedder,
		EmbeddingTimeout: time.Second, Timezone: "UTC", Limits: conflictassessment.DefaultSemanticAssessmentLimits(),
	})
	require.NoError(t, err)
	before := fixture.Snapshot(t)

	result, err := reviewer.ReviewRelationshipConflictCase(context.Background(), repository.ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, repository.ConflictReviewOutcomeNoop, result.Outcome)
	assert.Equal(t, "resolution_pending", result.Stage)
	assert.True(t, result.ResolutionPending)
	assert.Zero(t, embedder.calls)

	after := fixture.Snapshot(t)
	assert.Equal(t, before.RelationshipState, after.RelationshipState)
	assert.Equal(t, before.SearchDocumentState, after.SearchDocumentState)
	assert.Equal(t, before.EmbeddingJobState, after.EmbeddingJobState)
	assert.Equal(t, 0, after.CaseAttempts)
	assert.Empty(t, after.CaseLeaseWorkerID)
	assert.Nil(t, after.CaseLeaseUntil)
	eventCount, nextReviewAt := fixture.PendingState(t)
	assert.Equal(t, int64(1), eventCount)
	assert.True(t, nextReviewAt.After(fixture.ReviewNow))
	assert.WithinDuration(t, fixture.ReviewNow.Add(24*time.Hour), nextReviewAt, 2*time.Second)

	fixture.Reclaim(t)
	_, err = reviewer.ReviewRelationshipConflictCase(context.Background(), repository.ReviewRelationshipConflictCaseInput{
		TeamID: fixture.TeamID, WorkerID: fixture.WorkerID, ReviewRunID: fixture.ReviewRunID,
		ConflictID: fixture.ConflictID, Now: fixture.ReviewNow,
	})
	require.NoError(t, err)
	assert.Zero(t, embedder.calls)
	retryEventCount, _ := fixture.PendingState(t)
	assert.Equal(t, int64(1), retryEventCount)
}

type deterministicConflictProvider struct{}

func (deterministicConflictProvider) AssessRelationshipConflict(context.Context, conflictassessment.ConflictAssessmentRequest) (conflictassessment.ConflictAssessmentResponse, error) {
	return conflictassessment.ConflictAssessmentResponse{}, errors.New("deterministic conflict provider must not be called")
}

func (deterministicConflictProvider) ModelName() string { return "test-model" }

type deterministicConflictEmbeddingProvider struct {
	failed  bool
	timeout bool
	calls   int
}

func (p *deterministicConflictEmbeddingProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	p.calls++
	if p.timeout {
		<-ctx.Done()
		return nil, "test-model", ctx.Err()
	}
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

type blockingDeterministicConflictEmbeddingProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingDeterministicConflictEmbeddingProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, string, error) {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, "test-model", ctx.Err()
	}
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = []float32{1, 0, 0}
	}
	return vectors, "test-model", nil
}

func (*blockingDeterministicConflictEmbeddingProvider) ModelName() string { return "test-model" }
func (*blockingDeterministicConflictEmbeddingProvider) Dimensions() int   { return 3 }
func (*blockingDeterministicConflictEmbeddingProvider) IsAvailable() bool { return true }
