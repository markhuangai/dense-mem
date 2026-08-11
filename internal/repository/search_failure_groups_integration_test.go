package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSearchUpsertPreservesSameVersionFailedJobForReconciliation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-failed-reupsert-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-failed-reupsert-owner")
	insertSearchTestContract(t, adminDB, rls, "search-failed-reupsert", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	input := UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: uuid.NewString(),
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "same failed document",
	}
	first, err := repo.UpsertSearchDocument(ctx, input)
	require.NoError(t, err)
	claimed, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID: teamID, WorkerID: "failed-reupsert-worker", Limit: 1, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = repo.FailEmbeddingJob(ctx, FailEmbeddingJobInput{
		TeamID: teamID, EmbeddingJobID: claimed[0].EmbeddingJobID,
		WorkerID: "failed-reupsert-worker", ExpectedAttempts: claimed[0].Attempts,
		FailureClass: string(domain.EmbeddingFailureTransient),
		FailureCode:  string(domain.EmbeddingFailureProviderTimeout), Terminal: true,
	})
	require.NoError(t, err)

	second, err := repo.UpsertSearchDocument(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first.SearchDocumentID, second.SearchDocumentID)
	require.Equal(t, string(domain.SearchProjectionFailed), second.SearchState)
	require.Empty(t, second.QueuedJobID)

	var failedJobs int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND search_document_id = ?::uuid
			  AND status = 'failed'
		`, teamID, first.SearchDocumentID).Scan(&failedJobs).Error
	}))
	require.EqualValues(t, 1, failedJobs)
}

func TestSearchConvergenceDerivesFailureGroupLifecycleFromJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "derived-failure-group-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "derived-failure-group-owner")
	insertSearchTestContract(t, adminDB, rls, "derived-failure-group", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)

	document := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "derived failure group", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, document.SearchDocumentID, "derived-failure-writer")

	projection, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "attention_required", projection.Status)
	require.Len(t, projection.FailureGroups, 1)
	group := projection.FailureGroups[0]
	require.Equal(t, teamID, group.TeamID)
	require.Equal(t, "derived-failure-group-team", group.TeamName)
	require.Equal(t, string(domain.EmbeddingFailureProviderTimeout), group.FailureCode)
	require.Equal(t, "attention_required", group.Status)
	require.EqualValues(t, 1, group.FailedJobCount)
	require.Contains(t, group.Guidance, "automatic")

	now := databaseNowForTest(t, adminDB, rls)
	run, claimed, err := repo.ReserveEmbeddingReconciliationRun(ctx, ReserveEmbeddingReconciliationRunInput{
		EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate:        now,
		CreateIfMissing:     true,
		WorkerID:            "derived-failure-reconciler",
		Lease:               time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	canary, err := repo.SelectEmbeddingReconciliationCanary(ctx, SelectEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, EmbeddingContractID: contract.EmbeddingContractID,
		EmbeddingDimensions: contract.EmbeddingDimensions, CandidateCutoff: run.CandidateCutoff,
	})
	require.NoError(t, err)
	require.NotNil(t, canary)
	require.NoError(t, repo.MarkEmbeddingReconciliationCanaryAttempt(ctx, MarkEmbeddingReconciliationCanaryAttemptInput{
		TeamID: canary.TeamID, RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID,
		WorkerID: run.WorkerID, LeaseToken: run.LeaseToken, AttemptedAt: now, Lease: time.Minute,
	}))

	projection, err = repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "recovering", projection.Status)
	require.Len(t, projection.FailureGroups, 1)
	require.Equal(t, "recovering", projection.FailureGroups[0].Status)
	require.EqualValues(t, 1, projection.FailureGroups[0].ProcessingJobCount)

	require.NoError(t, repo.CompleteEmbeddingJob(ctx, CompleteEmbeddingJobInput{
		TeamID: canary.TeamID, EmbeddingJobID: canary.EmbeddingJobID,
		WorkerID:         EmbeddingReconciliationWorkerIDPrefix + run.RunID,
		ExpectedAttempts: canary.Attempts, Embedding: []float32{1, 0, 0},
	}))
	var recoveryCount int
	var recoveredAtPresent bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT recovery_count, last_recovered_at IS NOT NULL
			FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, canary.TeamID, canary.EmbeddingJobID).Row().Scan(&recoveryCount, &recoveredAtPresent)
	}))
	require.Equal(t, 1, recoveryCount)
	require.True(t, recoveredAtPresent)
	require.NoError(t, repo.CompleteEmbeddingReconciliationCanary(ctx, CompleteEmbeddingReconciliationCanaryInput{
		RunID: run.RunID, CanaryJobID: canary.EmbeddingJobID, WorkerID: run.WorkerID,
		LeaseToken: run.LeaseToken, Succeeded: true, RecoveredCount: 1,
	}))
	require.NoError(t, repo.CompleteEmbeddingReconciliationRun(ctx, CompleteEmbeddingReconciliationRunInput{
		RunID: run.RunID, WorkerID: run.WorkerID, LeaseToken: run.LeaseToken,
		Status: string(domain.EmbeddingReconciliationCompleted), CanaryOutcome: "succeeded", RecoveredCount: 1,
	}))

	projection, err = repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "converged", projection.Status)
	require.Empty(t, projection.FailureGroups)

	reopened := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "derived failure group reopened", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamID, reopened.SearchDocumentID, "derived-failure-reopen-writer")
	projection, err = repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.Equal(t, "attention_required", projection.Status)
	require.Len(t, projection.FailureGroups, 1)
	require.EqualValues(t, 1, projection.FailureGroups[0].FailedJobCount)
}

func TestSearchConvergenceKeepsSameFailureIdentitySeparatedByTeam(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "failure-group-team-a")
	teamB := createLedgerTeam(t, adminDB, rls, "failure-group-team-b")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "failure-group-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamB, "failure-group-owner-b")
	insertSearchTestContract(t, adminDB, rls, "failure-group-isolation", 3, "exact", "")
	repo := NewSearchRepository(appDB, rls)

	documentA := upsertSearchDocumentForTest(t, repo, teamA, ownerA, "team A provider timeout", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamA, documentA.SearchDocumentID, "failure-group-writer-a")
	documentB := upsertSearchDocumentForTest(t, repo, teamB, ownerB, "team B provider timeout", 1)
	failEmbeddingDocumentForReconciliationTest(t, ctx, repo, teamB, documentB.SearchDocumentID, "failure-group-writer-b")

	projection, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.EqualValues(t, 2, projection.FailureGroupCount)
	require.Len(t, projection.FailureGroups, 2)
	groupsByTeam := map[string]EmbeddingFailureGroup{}
	for _, group := range projection.FailureGroups {
		groupsByTeam[group.TeamID] = group
	}
	require.Equal(t, "failure-group-team-a", groupsByTeam[teamA].TeamName)
	require.Equal(t, "failure-group-team-b", groupsByTeam[teamB].TeamName)
	require.EqualValues(t, 1, groupsByTeam[teamA].AffectedJobCount)
	require.EqualValues(t, 1, groupsByTeam[teamB].AffectedJobCount)
}
