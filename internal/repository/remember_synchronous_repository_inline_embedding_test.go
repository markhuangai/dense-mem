package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSynchronousRememberEmbeddingPlanSkipsCurrentReusedEntityDocument(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-current-entity-plan", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-current-entity-plan-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-current-entity-plan-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-current-entity-plan-owner-b")
	semantic := NewSemanticRepository(appDB, rls)
	entity := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "concept", "Authoritative Dense-Mem")
	search := NewSearchRepository(appDB, rls)
	documentText := entitySearchProjectionText(entity.CanonicalName, entity.EntityKind)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerA, SourceKind: "entity", SourceID: entity.EntityID,
		SourceVersion: int64(entity.Version), ProjectionFormat: 1, DocumentText: documentText,
	})
	require.NoError(t, err)
	require.NotEmpty(t, document.QueuedJobID)
	workerID := "sync-remember-current-entity-plan"
	jobs, err := search.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: workerID, Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, search.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: jobs[0].EmbeddingJobID, WorkerID: workerID,
		ExpectedAttempts: jobs[0].Attempts, SpaceID: jobs[0].SpaceID, Embedding: []float32{1, 0, 0},
	}))

	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerB, "sync-current-entity-plan", "sync-current-entity-plan-hash", nil)
	configureSynchronousRememberEntityReuse(t, repo, &input, entity)
	require.NotNil(t, input.InlineEmbeddings)

	plannedHashes := make([]string, 0, len(input.InlineEmbeddings.Embeddings))
	for _, embedding := range input.InlineEmbeddings.Embeddings {
		plannedHashes = append(plannedHashes, embedding.DocumentHash)
	}
	assert.NotContains(t, plannedHashes, searchDocumentTextHash(documentText))
	assert.Contains(t, plannedHashes, searchDocumentTextHash(input.CreateIngest.Evidence[0].Content))
}

func configureSynchronousRememberEntityReuse(t *testing.T, repo *LedgerRepositoryImpl, input *SynchronousRememberCommitInput, entity *EntityRecord) {
	t.Helper()
	require.NotNil(t, input)
	require.NotNil(t, entity)
	originalBuildCommit := input.BuildCommit
	input.BuildCommit = func(created *CreateIngestResult, scope SubmissionAssessmentRunScope) (PersistSubmissionAssessmentInput, CommitSubmissionAssessmentInput, error) {
		persist, commit, err := originalBuildCommit(created, scope)
		if err != nil {
			return PersistSubmissionAssessmentInput{}, CommitSubmissionAssessmentInput{}, err
		}
		for index := range commit.EntityResolutions {
			if commit.EntityResolutions[index].Resolution.MentionRef != "orion" {
				continue
			}
			commit.EntityResolutions[index].Resolution.Action = string(domain.EntityResolutionReuse)
			commit.EntityResolutions[index].Resolution.EntityID = entity.EntityID
			commit.EntityResolutions[index].Resolution.ExactEntityID = entity.EntityID
			commit.EntityResolutions[index].Resolution.EntityKind = entity.EntityKind
			commit.EntityResolutions[index].Resolution.CanonicalName = entity.CanonicalName
		}
		return persist, commit, nil
	}
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, input)
}
