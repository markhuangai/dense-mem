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

func (r *SearchRepositoryImpl) ClaimEmbeddingJobs(
	ctx context.Context,
	input ClaimEmbeddingJobsInput,
) ([]EmbeddingJob, error) {
	input = normalizeClaimEmbeddingJobsInput(input)
	if err := validateClaimEmbeddingJobsInput(input); err != nil {
		return nil, err
	}
	jobs := []EmbeddingJob{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		if err := markStaleEmbeddingJobs(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := failExpiredMaxAttemptEmbeddingJobs(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := refreshRelationshipProjectionGenerationsForTeam(ctx, tx, input.TeamID); err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			WITH claimed AS (
				SELECT job.team_id, job.embedding_job_id
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
				  AND attempts < max_attempts
				  AND (
				      (job.status = 'queued' AND job.available_at <= now())
				      OR (job.status = 'processing' AND job.lease_until <= clock_timestamp())
				  )
				ORDER BY job.available_at ASC, job.created_at ASC, job.embedding_job_id ASC
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			),
			updated AS (
				UPDATE embedding_jobs AS job
				SET status = 'processing',
				    attempts = attempts + 1,
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
				          job.lease_until
			)
			SELECT updated.*, document.document_text
			FROM updated
			JOIN search_documents AS document
			  ON document.team_id = updated.team_id::uuid
			 AND document.search_document_id = updated.search_document_id::uuid
			ORDER BY updated.lease_until ASC, updated.embedding_job_id ASC
		`, input.TeamID, input.Limit, input.WorkerID, int(input.Lease.Seconds())).Rows()
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
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var dims int
		var contractID, sourceKind, projectionGenerationID string
		err := tx.WithContext(ctx).Raw(`
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
		active, err := embeddingContractHasActiveSearchGeneration(ctx, tx, contractID, dims)
		if err != nil {
			return err
		}
		if !active {
			if err := markEmbeddingJobTerminal(ctx, tx, input, string(domain.EmbeddingJobStale), "active search contract changed before embedding completion"); err != nil {
				return err
			}
			return ErrSearchContractMismatch
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
			return ErrSearchStaleVersion
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
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var attempts, maxAttempts int
		var sourceKind, projectionGenerationID string
		err := tx.WithContext(ctx).Raw(`
				SELECT attempts, max_attempts, source_kind, COALESCE(projection_generation_id::text, '')
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
			Status:      status,
			RetryAfter:  retryAfter,
			Terminal:    terminal,
			Attempts:    attempts,
			MaxAttempts: maxAttempts,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: fail embedding job: %w", err)
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
		if input.TeamID != "" {
			where = append(where, "team_id = ?::uuid")
			args = append(args, input.TeamID)
		}
		if input.EmbeddingContractID != "" {
			where = append(where, "embedding_contract_id = ?::uuid")
			args = append(args, input.EmbeddingContractID)
		}
		if input.EmbeddingDimensions > 0 {
			where = append(where, "embedding_dimensions = ?")
			args = append(args, input.EmbeddingDimensions)
		}
		query := fmt.Sprintf(`
			SELECT
			    COUNT(*) FILTER (WHERE status = 'queued') AS queued,
			    COUNT(*) FILTER (WHERE status = 'processing') AS processing,
			    COUNT(*) FILTER (WHERE status = 'completed') AS completed,
			    COUNT(*) FILTER (WHERE status = 'failed') AS failed,
			    COUNT(*) FILTER (WHERE status = 'stale') AS stale,
			    COUNT(*) FILTER (WHERE status = 'cancelled') AS cancelled,
			    COUNT(*) FILTER (WHERE status = 'processing' AND lease_until <= clock_timestamp()) AS expired_leases,
			    COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - MIN(created_at) FILTER (WHERE status IN ('queued', 'processing')))), 0) AS oldest_pending_seconds,
			    COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - MIN(lease_until) FILTER (WHERE status = 'processing' AND lease_until <= clock_timestamp()))), 0) AS oldest_lease_seconds
			FROM embedding_jobs
			WHERE %s
		`, strings.Join(where, " AND "))
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
		    lease_until = NULL
		WHERE team_id = ?::uuid
		  AND embedding_job_id = ?::uuid
		  AND worker_id = ?
		  AND status = 'processing'
		  AND attempts = ?
	`, status, message, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts).Error
}

func refreshRelationshipProjectionGeneration(ctx context.Context, tx *gorm.DB, teamID string, projectionGenerationID string) error {
	return tx.WithContext(ctx).Exec(`
		WITH counts AS (
			SELECT generation.team_id,
			       generation.projection_generation_id,
			       count(DISTINCT document.search_document_id) AS projected_count,
			       count(DISTINCT document.search_document_id) FILTER (
			           WHERE document.search_state = 'current'
			             AND document.embedding IS NOT NULL
			       ) AS current_vector_count,
			       count(DISTINCT job.embedding_job_id) FILTER (
			           WHERE job.status IN ('queued', 'processing')
			       ) AS unresolved_job_count,
			       count(DISTINCT job.embedding_job_id) FILTER (
			           WHERE job.status = 'failed'
			       ) AS failed_job_count
			FROM search_projection_generations AS generation
			LEFT JOIN search_documents AS document
			  ON document.team_id = generation.team_id
			 AND document.source_kind = 'relationship'
			 AND document.projection_format_version = generation.projection_format_version
			 AND document.projection_generation_id = generation.projection_generation_id
			LEFT JOIN embedding_jobs AS job
			  ON job.team_id = generation.team_id
			 AND job.source_kind = 'relationship'
			 AND job.projection_format_version = generation.projection_format_version
			 AND job.projection_generation_id = generation.projection_generation_id
			WHERE generation.team_id = ?::uuid
			  AND generation.projection_generation_id = ?::uuid
			  AND generation.source_kind = 'relationship'
			  AND generation.projection_format_version = 2
			GROUP BY generation.team_id, generation.projection_generation_id
		)
		UPDATE search_projection_generations AS generation
		SET projected_count = counts.projected_count,
		    current_vector_count = counts.current_vector_count,
		    failed_job_count = counts.failed_job_count,
		    state = CASE
		        WHEN counts.failed_job_count > 0 THEN 'failed'
		        WHEN generation.eligible_count = counts.projected_count
		         AND counts.projected_count = counts.current_vector_count
		         AND counts.unresolved_job_count = 0
		            THEN 'current'
		        WHEN generation.state = 'projecting_text' THEN 'embedding'
		        ELSE generation.state
		    END,
		    completed_at = CASE
		        WHEN generation.eligible_count = counts.projected_count
		         AND counts.unresolved_job_count = 0
		            THEN COALESCE(generation.completed_at, now())
		        ELSE generation.completed_at
		    END,
		    activated_at = CASE
		        WHEN counts.failed_job_count = 0
		         AND generation.eligible_count = counts.projected_count
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

func refreshRelationshipProjectionGenerationsForTeam(ctx context.Context, tx *gorm.DB, teamID string) error {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT projection_generation_id::text
		FROM search_projection_generations
		WHERE team_id = ?::uuid
		  AND source_kind = 'relationship'
		  AND projection_format_version = 2
		  AND state IN ('projecting_text', 'embedding')
	`, teamID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	generationIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		generationIDs = append(generationIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range generationIDs {
		if err := refreshRelationshipProjectionGeneration(ctx, tx, teamID, id); err != nil {
			return err
		}
	}
	return nil
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
		  AND job.status IN ('queued', 'processing')
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

func failExpiredMaxAttemptEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string) error {
	if err := tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs AS job
		SET status = 'failed',
		    error = ?,
		    completed_at = now(),
		    lease_until = NULL,
		    worker_id = '',
		    updated_at = now()
		WHERE job.team_id = ?::uuid
		  AND job.status = 'processing'
		  AND job.lease_until <= clock_timestamp()
		  AND job.attempts >= job.max_attempts
	`, embeddingJobAttemptsExhaustedMessage, teamID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE search_documents AS document
		SET search_state = 'failed',
		    embedding_error = ?,
		    updated_at = now()
		FROM embedding_jobs AS job
		WHERE job.team_id = ?::uuid
		  AND job.status = 'failed'
		  AND job.error = ?
		  AND document.team_id = job.team_id
			  AND document.search_document_id = job.search_document_id
			  AND document.source_version = job.source_version
			  AND document.projection_format_version = job.projection_format_version
			  AND document.projection_generation_id IS NOT DISTINCT FROM job.projection_generation_id
			  AND document.document_version = job.document_version
		  AND document.embedding_contract_id = job.embedding_contract_id
		  AND document.embedding_dimensions = job.embedding_dimensions
	`, embeddingJobAttemptsExhaustedMessage, teamID, embeddingJobAttemptsExhaustedMessage).Error
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

func boundedEmbeddingError(message string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if message == "" {
		return "embedding job failed"
	}
	const limit = 512
	if len(message) > limit {
		return message[:limit]
	}
	return message
}

func normalizeFailEmbeddingJobInput(input FailEmbeddingJobInput) FailEmbeddingJobInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingJobID = strings.TrimSpace(input.EmbeddingJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.Error = boundedEmbeddingError(input.Error)
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	if input.RetryAfter > 24*time.Hour {
		input.RetryAfter = 24 * time.Hour
	}
	return input
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
