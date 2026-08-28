package conflictreview

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
)

func TestServiceReleasesClaimWhenEmbeddingResponseIsInvalid(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.reviewResult = &repository.ReviewRelationshipConflictCaseResult{
		ConflictID: conflictReviewTestConflictID,
		Outcome:    repository.ConflictReviewOutcomeResolve,
		Resolution: &repository.RelationshipConflictResolutionInput{
			TeamID: conflictReviewTestTeamID, ConflictID: conflictReviewTestConflictID,
			ReviewRunID: conflictReviewTestReviewRunID, WorkerID: conflictReviewTestWorkerID,
			ExpectedCaseVersion: 3, PreferredPositionID: conflictReviewTestPositionAID,
			Method: "deterministic", Now: time.Now().UTC(),
		},
	}
	repo.planResult = &repository.RelationshipConflictResolutionPlan{
		Fence: repository.RelationshipConflictResolutionFence{
			EmbeddingContractID:     "00000000-0000-0000-0000-000000000601",
			EmbeddingDimensions:     2,
			EmbeddingModel:          "test-embedding-model",
			SearchIndexGenerationID: "00000000-0000-0000-0000-000000000602",
			IndexGeneration:         1,
		},
		Documents: []repository.RelationshipConflictResolutionDocument{{
			TeamID: conflictReviewTestTeamID, RelationshipID: conflictReviewTestConflictID,
			OwnerProfileID: conflictReviewTestTeamID, SpaceID: conflictReviewTestTeamID,
			SpaceGeneration: 1, SourceVersion: 1, DocumentHash: "hash-a", DocumentText: "relationship A",
		}},
	}
	embedder := &conflictReviewEmbeddingProviderStub{returnedModel: "unexpected-model"}
	service, err := New(Dependencies{
		Repository: repo, Provider: &conflictReviewProviderStub{}, Embeddings: embedder,
		EmbeddingTimeout: time.Second, Timezone: "UTC", Limits: conflictassessment.DefaultSemanticAssessmentLimits(),
	})
	require.NoError(t, err)

	_, err = service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.ErrorIs(t, err, semanticwrite.ErrProviderResponseInvalid)
	assert.Empty(t, repo.commitInputs)
	require.Len(t, repo.releaseInputs, 1)
	assert.Equal(t, conflictReviewTestConflictID, repo.releaseInputs[0].ConflictID)
}

func TestServiceReleasesClaimWhenResolutionBecomesStale(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.reviewResult = &repository.ReviewRelationshipConflictCaseResult{
		ConflictID: conflictReviewTestConflictID,
		Outcome:    repository.ConflictReviewOutcomeResolve,
		Resolution: &repository.RelationshipConflictResolutionInput{
			TeamID: conflictReviewTestTeamID, ConflictID: conflictReviewTestConflictID,
			ReviewRunID: conflictReviewTestReviewRunID, WorkerID: conflictReviewTestWorkerID,
			ExpectedCaseVersion: 3, PreferredPositionID: conflictReviewTestPositionAID,
			Method: "deterministic", Now: time.Now().UTC(),
		},
	}
	repo.planResult = &repository.RelationshipConflictResolutionPlan{
		Fence: repository.RelationshipConflictResolutionFence{
			EmbeddingContractID:     "00000000-0000-0000-0000-000000000601",
			EmbeddingDimensions:     2,
			EmbeddingModel:          "test-embedding-model",
			SearchIndexGenerationID: "00000000-0000-0000-0000-000000000602",
			IndexGeneration:         1,
		},
		Documents: []repository.RelationshipConflictResolutionDocument{{
			TeamID: conflictReviewTestTeamID, RelationshipID: conflictReviewTestConflictID,
			OwnerProfileID: conflictReviewTestTeamID, SpaceID: conflictReviewTestTeamID,
			SpaceGeneration: 1, SourceVersion: 1, DocumentHash: "hash-a", DocumentText: "relationship A",
		}},
	}
	repo.applyResult = &repository.ApplyOverdueConflictResolutionResult{Stale: true}
	service := newConflictReviewService(t, repo, &conflictReviewProviderStub{})

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	assert.Equal(t, repository.ConflictReviewOutcomeNoop, result.Outcome)
	assert.Equal(t, "resolution_stale", result.Stage)
	require.Len(t, repo.releaseInputs, 1)
	assert.Equal(t, conflictReviewTestConflictID, repo.releaseInputs[0].ConflictID)
}

func TestServiceBoundsDetachedClaimReleaseContext(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	service := &Service{repository: repo}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := service.releaseRetryableConflictClaim(ctx, repository.RelationshipConflictResolutionInput{
		TeamID: conflictReviewTestTeamID, ConflictID: conflictReviewTestConflictID,
		ReviewRunID: conflictReviewTestReviewRunID, WorkerID: conflictReviewTestWorkerID,
		Now: time.Now().UTC(),
	}, semanticwrite.ErrProviderUnavailable)
	require.NoError(t, err)
	require.Len(t, repo.releaseContexts, 1)
	deadline, ok := repo.releaseContexts[0].Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(conflictClaimReleaseTimeout), deadline, time.Second)
}

func TestServiceEmbedsOversizedResolutionInBoundedBatches(t *testing.T) {
	repo := newConflictReviewRepositoryStub(t)
	repo.reviewResult = &repository.ReviewRelationshipConflictCaseResult{
		ConflictID: conflictReviewTestConflictID,
		Outcome:    repository.ConflictReviewOutcomeResolve,
		Resolution: &repository.RelationshipConflictResolutionInput{
			TeamID: conflictReviewTestTeamID, ConflictID: conflictReviewTestConflictID,
			ReviewRunID: conflictReviewTestReviewRunID, WorkerID: conflictReviewTestWorkerID,
			ExpectedCaseVersion: 3, PreferredPositionID: conflictReviewTestPositionAID,
			Method: "deterministic", Now: time.Now().UTC(),
		},
	}
	repo.planResult = &repository.RelationshipConflictResolutionPlan{
		Fence: repository.RelationshipConflictResolutionFence{
			EmbeddingContractID:     "00000000-0000-0000-0000-000000000601",
			EmbeddingDimensions:     2,
			EmbeddingModel:          "test-embedding-model",
			SearchIndexGenerationID: "00000000-0000-0000-0000-000000000602",
			IndexGeneration:         1,
		},
		Documents: make([]repository.RelationshipConflictResolutionDocument, semanticwrite.MaxDocuments+1),
	}
	for index := range repo.planResult.Documents {
		repo.planResult.Documents[index] = repository.RelationshipConflictResolutionDocument{
			TeamID: conflictReviewTestTeamID, RelationshipID: conflictReviewTestConflictID,
			OwnerProfileID: conflictReviewTestTeamID, SpaceID: conflictReviewTestTeamID,
			SpaceGeneration: 1, SourceVersion: 1, DocumentHash: fmt.Sprintf("hash-%03d", index),
			DocumentText: fmt.Sprintf("relationship %03d", index),
		}
	}
	embedder := &conflictReviewEmbeddingProviderStub{}
	service := newConflictReviewService(t, repo, &conflictReviewProviderStub{})
	service.embeddings = semanticwrite.NewExecutor(conflictEmbeddingBatchProvider{provider: embedder})

	result, err := service.ReviewRelationshipConflictCase(context.Background(), conflictReviewInput())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, repository.ConflictReviewOutcomeResolve, result.Outcome)
	require.Len(t, embedder.batchSizes, 2)
	assert.Equal(t, []int{semanticwrite.MaxDocuments, 1}, embedder.batchSizes)
	require.Len(t, repo.commitInputs, 1)
	assert.Len(t, repo.commitInputs[0].Embeddings, semanticwrite.MaxDocuments+1)
}
