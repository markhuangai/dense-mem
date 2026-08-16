package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeClaimEmbeddingJobsInputAllowsConfiguredMaxBatch(t *testing.T) {
	input := normalizeClaimEmbeddingJobsInput(ClaimEmbeddingJobsInput{Limit: 300})
	assert.Equal(t, 256, input.Limit)
}

func TestSearchEmbeddingClaimLocksOnlyReturnedJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewSearchRepository(appDB, rls)

	claimWhileFirstTransactionIsOpen := func(t *testing.T, teamID string) []EmbeddingJob {
		t.Helper()
		release := make(chan struct{})
		var releaseOnce sync.Once
		closeRelease := func() {
			releaseOnce.Do(func() {
				close(release)
			})
		}
		t.Cleanup(closeRelease)
		firstClaimed := make(chan string, 1)
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
				var claimed []struct {
					EmbeddingJobID string `gorm:"column:embedding_job_id"`
				}
				if err := tx.Raw(
					claimEmbeddingJobsSQL,
					teamID,
					1,
					teamID,
					1,
					1,
					"holding-claim-worker",
					60,
				).Scan(&claimed).Error; err != nil {
					return err
				}
				if len(claimed) != 1 {
					return fmt.Errorf("holding claim returned %d jobs, want 1", len(claimed))
				}
				firstClaimed <- claimed[0].EmbeddingJobID
				<-release
				return nil
			})
		}()

		var firstID string
		select {
		case firstID = <-firstClaimed:
		case err := <-firstDone:
			require.NoError(t, err)
			t.Fatal("holding claim ended before acquiring a job")
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for holding claim")
		}

		second, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
			TeamID:   teamID,
			WorkerID: "second-claim-worker",
			Limit:    1,
			Lease:    time.Minute,
		})
		require.NoError(t, err)
		for _, job := range second {
			assert.NotEqual(t, firstID, job.EmbeddingJobID)
		}

		closeRelease()
		require.NoError(t, <-firstDone)
		return second
	}

	t.Run("does not lock the unused expired candidate", func(t *testing.T) {
		teamID := createLedgerTeam(t, adminDB, rls, "search-claim-lock-team")
		ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-claim-lock-owner")
		insertSearchTestContract(t, adminDB, rls, "search-claim-lock", 3, "exact", "")
		upsertSearchDocumentForTest(t, repo, teamID, ownerID, "queued claim candidate", 1)
		expired := upsertSearchDocumentForTest(t, repo, teamID, ownerID, "expired claim candidate", 1)
		err := rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
			return tx.Exec(`
				UPDATE embedding_jobs
				SET status = 'processing',
				    attempts = 1,
				    total_attempts = 1,
				    worker_id = 'expired-worker',
				    lease_until = now() - interval '1 minute'
				WHERE team_id = ?::uuid
				  AND embedding_job_id = ?::uuid
			`, teamID, expired.QueuedJobID).Error
		})
		require.NoError(t, err)

		require.Len(t, claimWhileFirstTransactionIsOpen(t, teamID), 1)
	})

	t.Run("skips a queued row locked by another claimer", func(t *testing.T) {
		teamID := createLedgerTeam(t, adminDB, rls, "search-claim-skip-locked-team")
		ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-claim-skip-locked-owner")
		upsertSearchDocumentForTest(t, repo, teamID, ownerID, "first queued claim candidate", 1)
		upsertSearchDocumentForTest(t, repo, teamID, ownerID, "second queued claim candidate", 1)

		require.Len(t, claimWhileFirstTransactionIsOpen(t, teamID), 1)
	})
}

func TestSearchEmbeddingClaimPlanStaysBoundedAtElevenThousandJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-claim-plan-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-claim-plan-owner")
	contractID := insertSearchTestContract(t, adminDB, rls, "search-claim-plan", 3, "exact", "")

	err := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID); err != nil {
			return err
		}
		if err := tx.Exec(`
			WITH generated AS MATERIALIZED (
				SELECT sequence,
				       md5('claim-plan-document-' || sequence::text)::uuid AS search_document_id,
				       md5('claim-plan-source-' || sequence::text)::uuid AS source_id,
				       md5('claim-plan-job-' || sequence::text)::uuid AS embedding_job_id
				FROM generate_series(1, 11000) AS sequence
			),
			documents AS (
				INSERT INTO search_documents (
				    team_id, search_document_id, owner_profile_id, source_kind,
				    source_id, source_version, projection_format_version,
				    projection_generation_id, document_version,
				    embedding_contract_id, embedding_dimensions, search_state,
				    document_text, document_hash
				)
				SELECT ?::uuid, generated.search_document_id, ?::uuid, 'evidence',
				       generated.source_id, 1, 1, NULL, 1, ?::uuid, 3, 'pending',
				       'claim plan document ' || generated.sequence::text,
				       md5('claim plan document ' || generated.sequence::text)
				FROM generated
				RETURNING search_document_id
			)
			INSERT INTO embedding_jobs (
			    team_id, embedding_job_id, search_document_id, owner_profile_id,
			    source_kind, source_id, source_version, projection_format_version,
			    projection_generation_id, document_version, embedding_contract_id,
			    embedding_dimensions, status, attempts, total_attempts, max_attempts, available_at,
			    lease_until, worker_id
			)
			SELECT ?::uuid, generated.embedding_job_id, generated.search_document_id,
			       ?::uuid, 'evidence', generated.source_id, 1, 1, NULL, 1,
			       ?::uuid, 3,
			       CASE WHEN generated.sequence BETWEEN 11 AND 20 THEN 'processing' ELSE 'queued' END,
			       CASE WHEN generated.sequence BETWEEN 11 AND 20 THEN 1 ELSE 0 END,
			       CASE WHEN generated.sequence BETWEEN 11 AND 20 THEN 1 ELSE 0 END,
			       20,
			       CASE
			           WHEN generated.sequence <= 10 THEN now() - interval '1 minute'
			           ELSE now() + interval '1 day'
			       END,
			       CASE
			           WHEN generated.sequence BETWEEN 11 AND 20 THEN now() - interval '1 minute'
			           ELSE NULL
			       END,
			       CASE
			           WHEN generated.sequence BETWEEN 11 AND 20 THEN 'expired-worker'
			           ELSE ''
			       END
			FROM generated
			JOIN documents USING (search_document_id)
		`, teamID, ownerID, contractID, teamID, ownerID, contractID).Error; err != nil {
			return err
		}
		return tx.Exec(`ANALYZE embedding_jobs`).Error
	})
	require.NoError(t, err)

	var plan []string
	err = rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			EXPLAIN (ANALYZE, BUFFERS, COSTS OFF)
			WITH `+embeddingJobCandidateCTEsSQL+`
			SELECT *
			FROM candidates
			ORDER BY available_at ASC, created_at ASC, embedding_job_id ASC
			LIMIT 64
		`, teamID, 64, teamID, 64).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan = append(plan, line)
		}
		return rows.Err()
	})
	require.NoError(t, err)
	joinedPlan := strings.Join(plan, "\n")
	assert.Contains(t, joinedPlan, "embedding_jobs_ready_idx")
	assert.Contains(t, joinedPlan, "embedding_jobs_lease_idx")
	assert.NotContains(t, joinedPlan, "Seq Scan on embedding_jobs")

	repo := NewSearchRepository(appDB, rls)
	jobs, err := repo.ClaimEmbeddingJobs(ctx, ClaimEmbeddingJobsInput{
		TeamID:   teamID,
		WorkerID: "claim-plan-worker",
		Limit:    64,
		Lease:    time.Minute,
	})
	require.NoError(t, err)
	assert.Len(t, jobs, 20)
}
