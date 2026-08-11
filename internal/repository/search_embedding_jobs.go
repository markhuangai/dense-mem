package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const EmbeddingReconciliationWorkerIDPrefix = "reconciliation:"

const embeddingJobCandidateCTEsSQL = `
queued_candidates AS MATERIALIZED (
	SELECT job.team_id, job.embedding_job_id, job.available_at, job.created_at
	FROM embedding_jobs AS job
	WHERE job.team_id = ?::uuid
	  AND job.status = 'queued'
	  AND job.available_at <= now()
	  AND job.attempts < job.max_attempts
	ORDER BY job.available_at ASC, job.created_at ASC, job.embedding_job_id ASC
	LIMIT ?
	FOR UPDATE SKIP LOCKED
),
expired_candidates AS MATERIALIZED (
	SELECT job.team_id, job.embedding_job_id, job.available_at, job.created_at
	FROM embedding_jobs AS job
	WHERE job.team_id = ?::uuid
	  AND job.status = 'processing'
	  AND job.lease_until <= clock_timestamp()
	  AND job.attempts < job.max_attempts
	ORDER BY job.lease_until ASC, job.created_at ASC, job.embedding_job_id ASC
	LIMIT GREATEST(? - (SELECT count(*) FROM queued_candidates), 0)
	FOR UPDATE SKIP LOCKED
),
candidates AS (
	SELECT * FROM queued_candidates
	UNION ALL
	SELECT * FROM expired_candidates
)`

const claimEmbeddingJobsSQL = `
WITH ` + embeddingJobCandidateCTEsSQL + `,
claimed AS (
	SELECT candidate.team_id, candidate.embedding_job_id
	FROM candidates AS candidate
	JOIN embedding_jobs AS job
	  ON job.team_id = candidate.team_id
	 AND job.embedding_job_id = candidate.embedding_job_id
	JOIN search_documents AS document
	  ON document.team_id = job.team_id
	 AND document.search_document_id = job.search_document_id
	 AND document.source_version = job.source_version
	 AND document.projection_format_version = job.projection_format_version
	 AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
	 AND document.document_version = job.document_version
	 AND document.embedding_contract_id = job.embedding_contract_id
	 AND document.embedding_dimensions = job.embedding_dimensions
	ORDER BY candidate.available_at ASC, candidate.created_at ASC, candidate.embedding_job_id ASC
	LIMIT ?
),
updated AS (
	UPDATE embedding_jobs AS job
	SET status = 'processing',
	    attempts = attempts + 1,
	    total_attempts = total_attempts + 1,
	    worker_id = ?,
	    lease_until = now() + make_interval(secs => ?::integer),
	    updated_at = now(),
	    error = ''
	FROM claimed
	WHERE job.team_id = claimed.team_id
	  AND job.embedding_job_id = claimed.embedding_job_id
	RETURNING job.team_id::text, job.embedding_job_id::text,
	          job.search_document_id::text, job.owner_profile_id::text,
	          job.source_kind, job.source_id::text, job.source_version,
	          job.projection_format_version, COALESCE(job.projection_generation_id::text, ''),
	          job.document_version, job.embedding_contract_id::text,
	          job.embedding_dimensions, job.status, job.attempts,
	          job.total_attempts, job.recovery_count, job.failure_class,
	          job.failure_code, job.first_failed_at, job.last_failed_at,
	          job.lease_until
)
SELECT updated.*, document.document_text
FROM updated
JOIN search_documents AS document
  ON document.team_id = updated.team_id::uuid
 AND document.search_document_id = updated.search_document_id::uuid
ORDER BY updated.lease_until ASC, updated.embedding_job_id ASC`

func (r *SearchRepositoryImpl) ClaimEmbeddingJobs(
	ctx context.Context,
	input ClaimEmbeddingJobsInput,
) ([]EmbeddingJob, error) {
	input = normalizeClaimEmbeddingJobsInput(input)
	if err := validateClaimEmbeddingJobsInput(input); err != nil {
		return nil, err
	}
	jobs := []EmbeddingJob{}
	err := r.withActiveTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		cleanupLimit := input.Limit * 2
		if cleanupLimit < 64 {
			cleanupLimit = 64
		}
		if err := markStaleClaimableEmbeddingJobs(ctx, tx, input.TeamID, cleanupLimit); err != nil {
			return err
		}
		if err := failExpiredMaxAttemptEmbeddingJobs(ctx, tx, input.TeamID, cleanupLimit); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(
			claimEmbeddingJobsSQL,
			input.TeamID,
			input.Limit,
			input.TeamID,
			input.Limit,
			input.Limit,
			input.WorkerID,
			int(input.Lease.Seconds()),
		).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job EmbeddingJob
			if err := rows.Scan(
				&job.TeamID,
				&job.EmbeddingJobID,
				&job.SearchDocumentID,
				&job.OwnerProfileID,
				&job.SourceKind,
				&job.SourceID,
				&job.SourceVersion,
				&job.ProjectionFormat,
				&job.ProjectionGenerationID,
				&job.DocumentVersion,
				&job.EmbeddingContractID,
				&job.EmbeddingDimensions,
				&job.Status,
				&job.Attempts,
				&job.TotalAttempts,
				&job.RecoveryCount,
				&job.FailureClass,
				&job.FailureCode,
				&job.FirstFailedAt,
				&job.LastFailedAt,
				&job.LeaseUntil,
				&job.DocumentText,
			); err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: claim embedding jobs: %w", err)
	}
	return jobs, nil
}

func (r *SearchRepositoryImpl) CompleteEmbeddingJob(ctx context.Context, input CompleteEmbeddingJobInput) error {
	input = normalizeCompleteEmbeddingJobInput(input)
	if err := validateCompleteEmbeddingJobInput(input); err != nil {
		return err
	}
	vectorLiteral, err := vectorLiteral(input.Embedding)
	if err != nil {
		return err
	}
	var terminalErr error
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		active, err := embeddingJobFinalizationTeamActive(ctx, tx, input.TeamID)
		if err != nil {
			return err
		}
		if !active {
			if err := markEmbeddingJobTerminal(ctx, tx, input, string(domain.EmbeddingJobStale), "team deleted before embedding finalization"); err != nil {
				return err
			}
			terminalErr = ErrTeamInactive
			return nil
		}
		if err := lockEmbeddingJobDocumentForFinalization(
			ctx, tx, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts,
		); err != nil {
			return err
		}
		var dims int
		var contractID, sourceKind, projectionGenerationID string
		err = tx.WithContext(ctx).Raw(`
				SELECT embedding_dimensions, embedding_contract_id::text,
				       source_kind, COALESCE(projection_generation_id::text, '')
				FROM embedding_jobs
				WHERE team_id = ?::uuid
				  AND embedding_job_id = ?::uuid
			  AND worker_id = ?
			  AND status = 'processing'
			  AND attempts = ?
			  AND lease_until > clock_timestamp()
				FOR UPDATE
			`, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts).Row().Scan(
			&dims,
			&contractID,
			&sourceKind,
			&projectionGenerationID,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if dims == 0 {
			return fmt.Errorf("%w: processing job not found or lease expired", ErrEmbeddingLeaseLost)
		}
		if dims != len(input.Embedding) {
			return fmt.Errorf("%w: job dimensions %d, vector dimensions %d", ErrSearchContractMismatch, dims, len(input.Embedding))
		}
		active, err = embeddingContractHasActiveSearchGeneration(ctx, tx, contractID, dims)
		if err != nil {
			return err
		}
		if !active {
			if err := markEmbeddingJobTerminal(ctx, tx, input, string(domain.EmbeddingJobStale), "active search contract changed before embedding completion"); err != nil {
				return err
			}
			terminalErr = ErrSearchContractMismatch
			return nil
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE search_documents AS document
			SET embedding = ?::vector,
			    search_state = 'current',
			    embedding_updated_at = now(),
			    embedding_error = '',
			    updated_at = now()
			FROM embedding_jobs AS job
			WHERE job.team_id = ?::uuid
			  AND job.embedding_job_id = ?::uuid
			  AND job.worker_id = ?
			  AND job.status = 'processing'
				  AND job.attempts = ?
				  AND document.team_id = job.team_id
				  AND document.search_document_id = job.search_document_id
				  AND document.source_version = job.source_version
				  AND document.projection_format_version = job.projection_format_version
				  AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
				  AND document.document_version = job.document_version
			  AND document.embedding_contract_id = job.embedding_contract_id
			  AND document.embedding_dimensions = job.embedding_dimensions
		`, vectorLiteral, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			if err := markEmbeddingJobTerminal(ctx, tx, input, string(domain.EmbeddingJobStale), "source or document version changed before embedding completion"); err != nil {
				return err
			}
			terminalErr = ErrSearchStaleVersion
			return nil
		}
		if err := markEmbeddingJobTerminal(ctx, tx, input, string(domain.EmbeddingJobCompleted), ""); err != nil {
			return err
		}
		if sourceKind == "relationship" && projectionGenerationID != "" {
			if err := refreshRelationshipProjectionGeneration(ctx, tx, input.TeamID, projectionGenerationID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("search: complete embedding job: %w", err)
	}
	if terminalErr != nil {
		return fmt.Errorf("search: complete embedding job: %w", terminalErr)
	}
	return nil
}

func (r *SearchRepositoryImpl) FailEmbeddingJob(
	ctx context.Context,
	input FailEmbeddingJobInput,
) (*EmbeddingJobFailureResult, error) {
	input = normalizeFailEmbeddingJobInput(input)
	if err := validateFailEmbeddingJobInput(input); err != nil {
		return nil, err
	}
	var result *EmbeddingJobFailureResult
	var terminalErr error
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		active, err := embeddingJobFinalizationTeamActive(ctx, tx, input.TeamID)
		if err != nil {
			return err
		}
		if !active {
			if err := markEmbeddingJobTerminal(ctx, tx, CompleteEmbeddingJobInput{
				TeamID:           input.TeamID,
				EmbeddingJobID:   input.EmbeddingJobID,
				WorkerID:         input.WorkerID,
				ExpectedAttempts: input.ExpectedAttempts,
			}, string(domain.EmbeddingJobStale), "team deleted before embedding finalization"); err != nil {
				return err
			}
			terminalErr = ErrTeamInactive
			return nil
		}
		if err := lockEmbeddingJobDocumentForFinalization(
			ctx, tx, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts,
		); err != nil {
			return err
		}
		var attempts, maxAttempts int
		var sourceKind, projectionGenerationID string
		err = tx.WithContext(ctx).Raw(`
				SELECT attempts, max_attempts, source_kind,
				       COALESCE(projection_generation_id::text, '')
				FROM embedding_jobs
				WHERE team_id = ?::uuid
				  AND embedding_job_id = ?::uuid
			  AND worker_id = ?
			  AND status = 'processing'
				AND attempts = ?
				AND lease_until > clock_timestamp()
				FOR UPDATE
			`, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts).Row().Scan(
			&attempts,
			&maxAttempts,
			&sourceKind,
			&projectionGenerationID,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEmbeddingLeaseLost
		}
		if err != nil {
			return err
		}
		documentCurrent, err := embeddingJobDocumentCurrent(ctx, tx, input.TeamID, input.EmbeddingJobID)
		if err != nil {
			return err
		}
		if !documentCurrent {
			if err := markEmbeddingJobTerminal(ctx, tx, CompleteEmbeddingJobInput{
				TeamID:           input.TeamID,
				EmbeddingJobID:   input.EmbeddingJobID,
				WorkerID:         input.WorkerID,
				ExpectedAttempts: input.ExpectedAttempts,
			}, string(domain.EmbeddingJobStale), "source or document version changed before embedding failure"); err != nil {
				return err
			}
			if sourceKind == "relationship" && projectionGenerationID != "" {
				if err := refreshRelationshipProjectionGeneration(ctx, tx, input.TeamID, projectionGenerationID); err != nil {
					return err
				}
			}
			result = &EmbeddingJobFailureResult{
				Status:      string(domain.EmbeddingJobStale),
				Terminal:    true,
				Stale:       true,
				Attempts:    attempts,
				MaxAttempts: maxAttempts,
			}
			return nil
		}
		terminal := input.Terminal || attempts >= maxAttempts
		status := string(domain.EmbeddingJobQueued)
		retryAfter := input.RetryAfter
		if retryAfter <= 0 {
			retryAfter = embeddingRetryBackoff(attempts)
		}
		failureClass := input.FailureClass
		failureCode := input.FailureCode
		completedExpr := "NULL"
		if terminal {
			status = string(domain.EmbeddingJobFailed)
			retryAfter = 0
			completedExpr = "now()"
		}
		query := fmt.Sprintf(`
			UPDATE embedding_jobs
			SET status = ?,
			    error = ?,
			    failure_class = ?,
			    failure_code = ?,
			    total_attempts = GREATEST(total_attempts, attempts),
			    first_failed_at = COALESCE(first_failed_at, now()),
			    last_failed_at = now(),
			    available_at = CASE
			        WHEN ?::integer > 0 THEN now() + (?::integer * interval '1 second')
			        ELSE now()
			    END,
			    lease_until = NULL,
			    worker_id = '',
			    completed_at = %s,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
			  AND worker_id = ?
			  AND status = 'processing'
			  AND attempts = ?
		`, completedExpr)
		retrySeconds := int(retryAfter.Seconds())
		update := tx.WithContext(ctx).Exec(
			query,
			status,
			input.Error,
			failureClass,
			failureCode,
			retrySeconds,
			retrySeconds,
			input.TeamID,
			input.EmbeddingJobID,
			input.WorkerID,
			input.ExpectedAttempts,
		)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrEmbeddingLeaseLost
		}
		if err := updateSearchDocumentAfterEmbeddingFailure(ctx, tx, input, status); err != nil {
			return err
		}
		if sourceKind == "relationship" && projectionGenerationID != "" {
			if err := refreshRelationshipProjectionGeneration(ctx, tx, input.TeamID, projectionGenerationID); err != nil {
				return err
			}
		}
		result = &EmbeddingJobFailureResult{
			Status:       status,
			RetryAfter:   retryAfter,
			Terminal:     terminal,
			Attempts:     attempts,
			MaxAttempts:  maxAttempts,
			FailureClass: failureClass,
			FailureCode:  failureCode,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: fail embedding job: %w", err)
	}
	if terminalErr != nil {
		return nil, fmt.Errorf("search: fail embedding job: %w", terminalErr)
	}
	return result, nil
}

func (r *SearchRepositoryImpl) GetEmbeddingQueueStats(
	ctx context.Context,
	input EmbeddingQueueStatsInput,
) (*EmbeddingQueueStats, error) {
	input = normalizeEmbeddingQueueStatsInput(input)
	if err := validateEmbeddingQueueStatsInput(input); err != nil {
		return nil, err
	}
	stats := &EmbeddingQueueStats{
		TeamID:              input.TeamID,
		EmbeddingContractID: input.EmbeddingContractID,
		EmbeddingDimensions: input.EmbeddingDimensions,
	}
	read := func(tx *gorm.DB) error {
		where := []string{"1 = 1"}
		args := []any{}
		from := "embedding_jobs AS job"
		if input.ActiveTeamsOnly {
			from += " JOIN teams AS team ON team.id = job.team_id AND team.status = 'active' AND team.deleted_at IS NULL"
		}
		if input.TeamID != "" {
			where = append(where, "job.team_id = ?::uuid")
			args = append(args, input.TeamID)
		}
		if input.EmbeddingContractID != "" {
			where = append(where, "job.embedding_contract_id = ?::uuid")
			args = append(args, input.EmbeddingContractID)
		}
		if input.EmbeddingDimensions > 0 {
			where = append(where, "job.embedding_dimensions = ?")
			args = append(args, input.EmbeddingDimensions)
		}
		query := fmt.Sprintf(`
			SELECT
			    COUNT(*) FILTER (WHERE job.status = 'queued') AS queued,
			    COUNT(*) FILTER (WHERE job.status = 'processing') AS processing,
			    COUNT(*) FILTER (WHERE job.status = 'completed') AS completed,
			    COUNT(*) FILTER (WHERE job.status = 'failed') AS failed,
			    COUNT(*) FILTER (WHERE job.status = 'stale') AS stale,
			    COUNT(*) FILTER (WHERE job.status = 'cancelled') AS cancelled,
			    COUNT(*) FILTER (WHERE job.status = 'processing' AND job.lease_until <= clock_timestamp()) AS expired_leases,
			    COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - MIN(job.created_at) FILTER (WHERE job.status IN ('queued', 'processing')))), 0) AS oldest_pending_seconds,
			    COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - MIN(job.lease_until) FILTER (WHERE job.status = 'processing' AND job.lease_until <= clock_timestamp()))), 0) AS oldest_lease_seconds
			FROM %s
			WHERE %s
		`, from, strings.Join(where, " AND "))
		var oldestPendingSeconds, oldestLeaseSeconds float64
		if err := tx.WithContext(ctx).Raw(query, args...).Row().Scan(
			&stats.Queued,
			&stats.Processing,
			&stats.Completed,
			&stats.Failed,
			&stats.Stale,
			&stats.Cancelled,
			&stats.ExpiredLeases,
			&oldestPendingSeconds,
			&oldestLeaseSeconds,
		); err != nil {
			return err
		}
		stats.OldestPendingAge = time.Duration(oldestPendingSeconds * float64(time.Second))
		stats.OldestLeaseAge = time.Duration(oldestLeaseSeconds * float64(time.Second))
		stats.TerminalFailures = stats.Failed
		stats.CutoverBlocking = stats.Queued+stats.Processing+stats.TerminalFailures > 0
		return nil
	}
	var err error
	if input.TeamID != "" {
		err = r.withTeamTx(ctx, input.TeamID, read)
	} else {
		err = r.withSystemTx(ctx, read)
	}
	if err != nil {
		return nil, fmt.Errorf("search: embedding queue stats: %w", err)
	}
	return stats, nil
}

func markEmbeddingJobTerminal(ctx context.Context, tx *gorm.DB, input CompleteEmbeddingJobInput, status string, message string) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs
		SET status = ?,
		    error = ?,
		    completed_at = now(),
		    updated_at = now(),
		    lease_until = NULL,
		    recovery_count = recovery_count + CASE WHEN ? = 'completed' AND ? LIKE ? THEN 1 ELSE 0 END,
		    last_recovered_at = CASE WHEN ? = 'completed' AND ? LIKE ? THEN now() ELSE last_recovered_at END
		WHERE team_id = ?::uuid
		  AND embedding_job_id = ?::uuid
		  AND worker_id = ?
		  AND status = 'processing'
		  AND attempts = ?
		`, status, message, status, input.WorkerID, EmbeddingReconciliationWorkerIDPrefix+"%", status, input.WorkerID, EmbeddingReconciliationWorkerIDPrefix+"%",
		input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts).Error
}

func embeddingJobFinalizationTeamActive(ctx context.Context, tx *gorm.DB, teamID string) (bool, error) {
	var active bool
	err := tx.WithContext(ctx).Raw(`
		SELECT status = 'active' AND deleted_at IS NULL
		FROM teams
		WHERE id = ?::uuid
		FOR SHARE
	`, teamID).Row().Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

func lockEmbeddingJobDocumentForFinalization(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	embeddingJobID string,
	workerID string,
	expectedAttempts int,
) error {
	var documentID string
	err := tx.WithContext(ctx).Raw(`
		SELECT document.search_document_id::text
		FROM embedding_jobs AS job
		JOIN search_documents AS document
		  ON document.team_id = job.team_id
		 AND document.search_document_id = job.search_document_id
		WHERE job.team_id = ?::uuid
		  AND job.embedding_job_id = ?::uuid
		  AND job.worker_id = ?
		  AND job.status = 'processing'
		  AND job.attempts = ?
		  AND job.lease_until > clock_timestamp()
		FOR UPDATE OF document
	`, teamID, embeddingJobID, workerID, expectedAttempts).Row().Scan(&documentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEmbeddingLeaseLost
	}
	return err
}

func refreshRelationshipProjectionGeneration(ctx context.Context, tx *gorm.DB, teamID string, projectionGenerationID string) error {
	return tx.WithContext(ctx).Exec(`
		WITH generation_row AS (
			SELECT team_id, projection_generation_id, projection_format_version, state
			FROM search_projection_generations
			WHERE team_id = ?::uuid
			  AND projection_generation_id = ?::uuid
			  AND source_kind = 'relationship'
			  AND projection_format_version = 2
		),
		documents AS (
			SELECT count(document.search_document_id) AS projected_count,
			       count(document.search_document_id) FILTER (
			           WHERE document.search_state = 'current'
			             AND document.embedding IS NOT NULL
			       ) AS current_vector_count
			FROM generation_row AS generation
			LEFT JOIN search_documents AS document
			  ON document.team_id = generation.team_id
			 AND document.source_kind = 'relationship'
			 AND document.projection_format_version = generation.projection_format_version
			 AND document.projection_generation_id = generation.projection_generation_id
		),
		jobs AS (
			SELECT count(job.embedding_job_id) FILTER (
			           WHERE job.status IN ('queued', 'processing')
			       ) AS unresolved_job_count,
			       count(job.embedding_job_id) FILTER (
			           WHERE job.status = 'failed'
			       ) AS failed_job_count
			FROM generation_row AS generation
			LEFT JOIN embedding_jobs AS job
			  ON job.team_id = generation.team_id
			 AND job.source_kind = 'relationship'
			 AND job.projection_format_version = generation.projection_format_version
			 AND job.projection_generation_id = generation.projection_generation_id
		),
		counts AS (
			SELECT generation.team_id,
			       generation.projection_generation_id,
			       documents.projected_count AS eligible_count,
			       documents.projected_count,
			       documents.current_vector_count,
			       jobs.unresolved_job_count,
			       jobs.failed_job_count
			FROM generation_row AS generation
			CROSS JOIN documents
			CROSS JOIN jobs
		)
		UPDATE search_projection_generations AS generation
		SET eligible_count = CASE
		        WHEN generation.state = 'current' THEN generation.eligible_count
		        ELSE counts.eligible_count
		    END,
		    projected_count = counts.projected_count,
		    current_vector_count = counts.current_vector_count,
		    failed_job_count = counts.failed_job_count,
		    state = CASE
		        WHEN generation.state = 'current' THEN generation.state
		        WHEN counts.failed_job_count > 0 THEN 'failed'
		        WHEN counts.eligible_count = counts.projected_count
		         AND counts.projected_count = counts.current_vector_count
		         AND counts.unresolved_job_count = 0
		            THEN 'current'
		        WHEN generation.state IN ('projecting_text', 'failed') THEN 'embedding'
		        ELSE generation.state
		    END,
		    completed_at = CASE
		        WHEN counts.eligible_count = counts.projected_count
		         AND counts.unresolved_job_count = 0
		            THEN COALESCE(generation.completed_at, now())
		        ELSE generation.completed_at
		    END,
		    activated_at = CASE
		        WHEN counts.failed_job_count = 0
		         AND generation.state <> 'current'
		         AND counts.eligible_count = counts.projected_count
		         AND counts.projected_count = counts.current_vector_count
		         AND counts.unresolved_job_count = 0
		            THEN COALESCE(generation.activated_at, now())
		        ELSE generation.activated_at
		    END,
		    last_error = CASE
		        WHEN counts.failed_job_count > 0 THEN 'relationship projection generation has failed embedding jobs'
		        ELSE ''
		    END,
		    updated_at = now()
		FROM counts
		WHERE generation.team_id = counts.team_id
		  AND generation.projection_generation_id = counts.projection_generation_id
	`, teamID, projectionGenerationID).Error
}

func markStaleClaimableEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string, limit int) error {
	return tx.WithContext(ctx).Exec(`
		WITH queued_candidates AS MATERIALIZED (
			SELECT job.team_id, job.embedding_job_id, job.available_at, job.created_at
			FROM embedding_jobs AS job
			WHERE job.team_id = ?::uuid
			  AND job.status = 'queued'
			  AND job.available_at <= now()
			ORDER BY job.available_at ASC, job.created_at ASC, job.embedding_job_id ASC
			LIMIT ?
		),
		expired_candidates AS MATERIALIZED (
			SELECT job.team_id, job.embedding_job_id, job.available_at, job.created_at
			FROM embedding_jobs AS job
			WHERE job.team_id = ?::uuid
			  AND job.status = 'processing'
			  AND job.lease_until <= clock_timestamp()
			ORDER BY job.lease_until ASC, job.created_at ASC, job.embedding_job_id ASC
			LIMIT ?
		),
		candidates AS (
			SELECT * FROM queued_candidates
			UNION ALL
			SELECT * FROM expired_candidates
		),
		stale AS (
			SELECT job.team_id, job.embedding_job_id
			FROM candidates AS candidate
			JOIN embedding_jobs AS job
			  ON job.team_id = candidate.team_id
			 AND job.embedding_job_id = candidate.embedding_job_id
			WHERE NOT EXISTS (
			    SELECT 1
			    FROM search_documents AS document
			    WHERE document.team_id = job.team_id
			      AND document.search_document_id = job.search_document_id
			      AND document.source_version = job.source_version
			      AND document.projection_format_version = job.projection_format_version
			      AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
			      AND document.document_version = job.document_version
			      AND document.embedding_contract_id = job.embedding_contract_id
			      AND document.embedding_dimensions = job.embedding_dimensions
			)
			ORDER BY candidate.available_at ASC, candidate.created_at ASC, candidate.embedding_job_id ASC
			LIMIT ?
			FOR UPDATE OF job SKIP LOCKED
		)
		UPDATE embedding_jobs AS job
		SET status = 'stale',
		    error = 'source or document version changed before embedding claim',
		    completed_at = now(),
		    lease_until = NULL,
		    worker_id = '',
		    updated_at = now()
		FROM stale
		WHERE job.team_id = stale.team_id
		  AND job.embedding_job_id = stale.embedding_job_id
	`, teamID, limit, teamID, limit, limit).Error
}

func markStaleEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs AS job
		SET status = 'stale',
		    error = 'source or document version changed before embedding claim',
		    completed_at = now(),
		    lease_until = NULL,
		    worker_id = '',
		    updated_at = now()
		WHERE job.team_id = ?::uuid
		  AND job.status IN ('queued', 'processing', 'failed')
		  AND NOT EXISTS (
		      SELECT 1
		      FROM search_documents AS document
		      WHERE document.team_id = job.team_id
		        AND document.search_document_id = job.search_document_id
		        AND document.source_version = job.source_version
		        AND document.projection_format_version = job.projection_format_version
		        AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
		        AND document.document_version = job.document_version
		        AND document.embedding_contract_id = job.embedding_contract_id
		        AND document.embedding_dimensions = job.embedding_dimensions
		  )
	`, teamID).Error
}

func embeddingContractHasActiveSearchGeneration(ctx context.Context, tx *gorm.DB, contractID string, dimensions int) (bool, error) {
	var active bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
		    SELECT 1
		    FROM search_index_generations AS generation
		    JOIN embedding_contracts AS contract
		      ON contract.embedding_contract_id = generation.embedding_contract_id
		     AND contract.dimensions = generation.embedding_dimensions
		    WHERE generation.embedding_contract_id = ?::uuid
		      AND generation.embedding_dimensions = ?
		      AND generation.activation_state = 'active'
		      AND contract.lifecycle_state = 'active'
		)
	`, contractID, dimensions).Scan(&active).Error
	return active, err
}

func embeddingJobDocumentCurrent(ctx context.Context, tx *gorm.DB, teamID string, embeddingJobID string) (bool, error) {
	var current bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
		    SELECT 1
		    FROM embedding_jobs AS job
		    JOIN search_documents AS document
		      ON document.team_id = job.team_id
			     AND document.search_document_id = job.search_document_id
			     AND document.source_version = job.source_version
			     AND document.projection_format_version = job.projection_format_version
			     AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
			     AND document.document_version = job.document_version
		     AND document.embedding_contract_id = job.embedding_contract_id
		     AND document.embedding_dimensions = job.embedding_dimensions
		    WHERE job.team_id = ?::uuid
		      AND job.embedding_job_id = ?::uuid
		)
	`, teamID, embeddingJobID).Scan(&current).Error
	return current, err
}

func updateSearchDocumentAfterEmbeddingFailure(ctx context.Context, tx *gorm.DB, input FailEmbeddingJobInput, status string) error {
	searchState := string(domain.SearchProjectionPending)
	if status == string(domain.EmbeddingJobFailed) {
		searchState = string(domain.SearchProjectionFailed)
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE search_documents AS document
		SET search_state = ?,
		    embedding_error = ?,
		    updated_at = now()
		FROM embedding_jobs AS job
		WHERE job.team_id = ?::uuid
		  AND job.embedding_job_id = ?::uuid
		  AND document.team_id = job.team_id
			  AND document.search_document_id = job.search_document_id
			  AND document.source_version = job.source_version
			  AND document.projection_format_version = job.projection_format_version
			  AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
			  AND document.document_version = job.document_version
		  AND document.embedding_contract_id = job.embedding_contract_id
		  AND document.embedding_dimensions = job.embedding_dimensions
	`, searchState, input.Error, input.TeamID, input.EmbeddingJobID).Error
}

func embeddingRetryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	seconds := 5
	for i := 1; i < attempts; i++ {
		seconds *= 2
		if seconds >= 300 {
			return 5 * time.Minute
		}
	}
	return time.Duration(seconds) * time.Second
}

func normalizeFailEmbeddingJobInput(input FailEmbeddingJobInput) FailEmbeddingJobInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingJobID = strings.TrimSpace(input.EmbeddingJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	if input.RetryAfter > 24*time.Hour {
		input.RetryAfter = 24 * time.Hour
	}
	input.FailureClass, input.FailureCode = normalizeEmbeddingFailureContract(input.FailureClass, input.FailureCode)
	input.Error = domain.EmbeddingFailureMessage(input.FailureCode)
	return input
}

func normalizeEmbeddingFailureContract(failureClass, failureCode string) (string, string) {
	failureClass = strings.TrimSpace(failureClass)
	failureCode = strings.TrimSpace(failureCode)
	validClass := false
	for _, value := range domain.EmbeddingFailureClasses() {
		if value == failureClass {
			validClass = true
			break
		}
	}
	validCode := false
	for _, value := range domain.EmbeddingFailureCodes() {
		if value == failureCode {
			validCode = true
			break
		}
	}
	if !validClass || !validCode {
		return string(domain.EmbeddingFailurePermanent), string(domain.EmbeddingFailureUnknown)
	}
	return failureClass, failureCode
}

func validateFailEmbeddingJobInput(input FailEmbeddingJobInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.EmbeddingJobID); err != nil {
		return fmt.Errorf("embedding_job_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.ExpectedAttempts < 1 {
		return errors.New("expected_attempts must be greater than zero")
	}
	if input.Error == "" {
		return errors.New("error is required")
	}
	return nil
}

func normalizeEmbeddingQueueStatsInput(input EmbeddingQueueStatsInput) EmbeddingQueueStatsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	return input
}

func validateEmbeddingQueueStatsInput(input EmbeddingQueueStatsInput) error {
	if input.TeamID != "" {
		if _, err := uuid.Parse(input.TeamID); err != nil {
			return fmt.Errorf("team_id is invalid: %w", err)
		}
	}
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	if input.EmbeddingDimensions < 0 {
		return errors.New("embedding_dimensions must not be negative")
	}
	return nil
}
