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

func (r *V2SearchRepositoryImpl) ClaimEmbeddingJobs(
	ctx context.Context,
	input V2ClaimEmbeddingJobsInput,
) ([]V2EmbeddingJob, error) {
	input = normalizeV2ClaimEmbeddingJobsInput(input)
	if err := validateV2ClaimEmbeddingJobsInput(input); err != nil {
		return nil, err
	}
	jobs := []V2EmbeddingJob{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		if err := markV2StaleEmbeddingJobs(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := failV2ExpiredMaxAttemptEmbeddingJobs(ctx, tx, input.TeamID); err != nil {
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
			var job V2EmbeddingJob
			if err := rows.Scan(
				&job.TeamID,
				&job.EmbeddingJobID,
				&job.SearchDocumentID,
				&job.OwnerProfileID,
				&job.SourceKind,
				&job.SourceID,
				&job.SourceVersion,
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
		return nil, fmt.Errorf("v2 search: claim embedding jobs: %w", err)
	}
	return jobs, nil
}

func (r *V2SearchRepositoryImpl) CompleteEmbeddingJob(ctx context.Context, input V2CompleteEmbeddingJobInput) error {
	input = normalizeV2CompleteEmbeddingJobInput(input)
	if err := validateV2CompleteEmbeddingJobInput(input); err != nil {
		return err
	}
	vectorLiteral, err := v2VectorLiteral(input.Embedding)
	if err != nil {
		return err
	}
	err = r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var dims int
		var contractID string
		err := tx.WithContext(ctx).Raw(`
			SELECT embedding_dimensions, embedding_contract_id::text
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
			  AND worker_id = ?
			  AND status = 'processing'
			  AND attempts = ?
			  AND lease_until > clock_timestamp()
			FOR UPDATE
		`, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts).Row().Scan(&dims, &contractID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if dims == 0 {
			return fmt.Errorf("%w: processing job not found or lease expired", ErrV2EmbeddingLeaseLost)
		}
		if dims != len(input.Embedding) {
			return fmt.Errorf("%w: job dimensions %d, vector dimensions %d", ErrV2SearchProfileMismatch, dims, len(input.Embedding))
		}
		active, err := v2EmbeddingContractHasActiveSearchProfile(ctx, tx, contractID, dims)
		if err != nil {
			return err
		}
		if !active {
			if err := markV2EmbeddingJobTerminal(ctx, tx, input, string(domain.V2EmbeddingJobStale), "active search profile changed before embedding completion"); err != nil {
				return err
			}
			return ErrV2SearchProfileMismatch
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
			  AND document.document_version = job.document_version
			  AND document.embedding_contract_id = job.embedding_contract_id
			  AND document.embedding_dimensions = job.embedding_dimensions
		`, vectorLiteral, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			if err := markV2EmbeddingJobTerminal(ctx, tx, input, string(domain.V2EmbeddingJobStale), "source or document version changed before embedding completion"); err != nil {
				return err
			}
			return ErrV2SearchStaleVersion
		}
		if err := markV2EmbeddingJobTerminal(ctx, tx, input, string(domain.V2EmbeddingJobCompleted), ""); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("v2 search: complete embedding job: %w", err)
	}
	return nil
}

func (r *V2SearchRepositoryImpl) FailEmbeddingJob(
	ctx context.Context,
	input V2FailEmbeddingJobInput,
) (*V2EmbeddingJobFailureResult, error) {
	input = normalizeV2FailEmbeddingJobInput(input)
	if err := validateV2FailEmbeddingJobInput(input); err != nil {
		return nil, err
	}
	var result *V2EmbeddingJobFailureResult
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var attempts, maxAttempts int
		err := tx.WithContext(ctx).Raw(`
			SELECT attempts, max_attempts
			FROM embedding_jobs
			WHERE team_id = ?::uuid
			  AND embedding_job_id = ?::uuid
			  AND worker_id = ?
			  AND status = 'processing'
			  AND attempts = ?
			  AND lease_until > clock_timestamp()
			FOR UPDATE
		`, input.TeamID, input.EmbeddingJobID, input.WorkerID, input.ExpectedAttempts).Row().Scan(&attempts, &maxAttempts)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrV2EmbeddingLeaseLost
		}
		if err != nil {
			return err
		}
		documentCurrent, err := v2EmbeddingJobDocumentCurrent(ctx, tx, input.TeamID, input.EmbeddingJobID)
		if err != nil {
			return err
		}
		if !documentCurrent {
			if err := markV2EmbeddingJobTerminal(ctx, tx, V2CompleteEmbeddingJobInput{
				TeamID:           input.TeamID,
				EmbeddingJobID:   input.EmbeddingJobID,
				WorkerID:         input.WorkerID,
				ExpectedAttempts: input.ExpectedAttempts,
			}, string(domain.V2EmbeddingJobStale), "source or document version changed before embedding failure"); err != nil {
				return err
			}
			result = &V2EmbeddingJobFailureResult{
				Status:      string(domain.V2EmbeddingJobStale),
				Terminal:    true,
				Stale:       true,
				Attempts:    attempts,
				MaxAttempts: maxAttempts,
			}
			return nil
		}
		terminal := input.Terminal || attempts >= maxAttempts
		status := string(domain.V2EmbeddingJobQueued)
		retryAfter := input.RetryAfter
		if retryAfter <= 0 {
			retryAfter = v2EmbeddingRetryBackoff(attempts)
		}
		completedExpr := "NULL"
		if terminal {
			status = string(domain.V2EmbeddingJobFailed)
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
			return ErrV2EmbeddingLeaseLost
		}
		if err := updateV2SearchDocumentAfterEmbeddingFailure(ctx, tx, input, status); err != nil {
			return err
		}
		result = &V2EmbeddingJobFailureResult{
			Status:      status,
			RetryAfter:  retryAfter,
			Terminal:    terminal,
			Attempts:    attempts,
			MaxAttempts: maxAttempts,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 search: fail embedding job: %w", err)
	}
	return result, nil
}

func (r *V2SearchRepositoryImpl) GetEmbeddingQueueStats(
	ctx context.Context,
	input V2EmbeddingQueueStatsInput,
) (*V2EmbeddingQueueStats, error) {
	input = normalizeV2EmbeddingQueueStatsInput(input)
	if err := validateV2EmbeddingQueueStatsInput(input); err != nil {
		return nil, err
	}
	stats := &V2EmbeddingQueueStats{
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
		return nil, fmt.Errorf("v2 search: embedding queue stats: %w", err)
	}
	return stats, nil
}

func markV2EmbeddingJobTerminal(ctx context.Context, tx *gorm.DB, input V2CompleteEmbeddingJobInput, status string, message string) error {
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

func markV2StaleEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string) error {
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
		        AND document.document_version = job.document_version
		        AND document.embedding_contract_id = job.embedding_contract_id
		        AND document.embedding_dimensions = job.embedding_dimensions
		  )
	`, teamID).Error
}

func failV2ExpiredMaxAttemptEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string) error {
	if err := tx.WithContext(ctx).Exec(`
		UPDATE embedding_jobs AS job
		SET status = 'failed',
		    error = 'embedding lease expired after max attempts',
		    completed_at = now(),
		    lease_until = NULL,
		    worker_id = '',
		    updated_at = now()
		WHERE job.team_id = ?::uuid
		  AND job.status = 'processing'
		  AND job.lease_until <= clock_timestamp()
		  AND job.attempts >= job.max_attempts
	`, teamID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE search_documents AS document
		SET search_state = 'failed',
		    embedding_error = 'embedding lease expired after max attempts',
		    updated_at = now()
		FROM embedding_jobs AS job
		WHERE job.team_id = ?::uuid
		  AND job.status = 'failed'
		  AND job.error = 'embedding lease expired after max attempts'
		  AND document.team_id = job.team_id
		  AND document.search_document_id = job.search_document_id
		  AND document.source_version = job.source_version
		  AND document.document_version = job.document_version
		  AND document.embedding_contract_id = job.embedding_contract_id
		  AND document.embedding_dimensions = job.embedding_dimensions
	`, teamID).Error
}

func v2EmbeddingContractHasActiveSearchProfile(ctx context.Context, tx *gorm.DB, contractID string, dimensions int) (bool, error) {
	var active bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
		    SELECT 1
		    FROM search_index_profiles AS search
		    JOIN embedding_contracts AS contract
		      ON contract.embedding_contract_id = search.embedding_contract_id
		     AND contract.dimensions = search.embedding_dimensions
		    JOIN ranking_profiles AS ranking
		      ON ranking.profile_key = search.profile_key
		     AND ranking.activation_state = 'active'
		    WHERE search.embedding_contract_id = ?::uuid
		      AND search.embedding_dimensions = ?
		      AND search.activation_state = 'active'
		      AND contract.lifecycle_state = 'active'
		)
	`, contractID, dimensions).Scan(&active).Error
	return active, err
}

func v2EmbeddingJobDocumentCurrent(ctx context.Context, tx *gorm.DB, teamID string, embeddingJobID string) (bool, error) {
	var current bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
		    SELECT 1
		    FROM embedding_jobs AS job
		    JOIN search_documents AS document
		      ON document.team_id = job.team_id
		     AND document.search_document_id = job.search_document_id
		     AND document.source_version = job.source_version
		     AND document.document_version = job.document_version
		     AND document.embedding_contract_id = job.embedding_contract_id
		     AND document.embedding_dimensions = job.embedding_dimensions
		    WHERE job.team_id = ?::uuid
		      AND job.embedding_job_id = ?::uuid
		)
	`, teamID, embeddingJobID).Scan(&current).Error
	return current, err
}

func updateV2SearchDocumentAfterEmbeddingFailure(ctx context.Context, tx *gorm.DB, input V2FailEmbeddingJobInput, status string) error {
	searchState := string(domain.V2SearchProjectionPending)
	if status == string(domain.V2EmbeddingJobFailed) {
		searchState = string(domain.V2SearchProjectionFailed)
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
		  AND document.document_version = job.document_version
		  AND document.embedding_contract_id = job.embedding_contract_id
		  AND document.embedding_dimensions = job.embedding_dimensions
	`, searchState, input.Error, input.TeamID, input.EmbeddingJobID).Error
}

func v2EmbeddingRetryBackoff(attempts int) time.Duration {
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

func boundedV2EmbeddingError(message string) string {
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

func normalizeV2ClaimEmbeddingJobsInput(input V2ClaimEmbeddingJobsInput) V2ClaimEmbeddingJobsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	if input.Limit <= 0 {
		input.Limit = 10
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	if input.Lease <= 0 {
		input.Lease = time.Minute
	}
	return input
}

func validateV2ClaimEmbeddingJobsInput(input V2ClaimEmbeddingJobsInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	return nil
}

func normalizeV2CompleteEmbeddingJobInput(input V2CompleteEmbeddingJobInput) V2CompleteEmbeddingJobInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingJobID = strings.TrimSpace(input.EmbeddingJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	return input
}

func validateV2CompleteEmbeddingJobInput(input V2CompleteEmbeddingJobInput) error {
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
	if len(input.Embedding) == 0 {
		return errors.New("embedding is required")
	}
	return nil
}

func normalizeV2FailEmbeddingJobInput(input V2FailEmbeddingJobInput) V2FailEmbeddingJobInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingJobID = strings.TrimSpace(input.EmbeddingJobID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.Error = boundedV2EmbeddingError(input.Error)
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	if input.RetryAfter > 24*time.Hour {
		input.RetryAfter = 24 * time.Hour
	}
	return input
}

func validateV2FailEmbeddingJobInput(input V2FailEmbeddingJobInput) error {
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

func normalizeV2EmbeddingQueueStatsInput(input V2EmbeddingQueueStatsInput) V2EmbeddingQueueStatsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	return input
}

func validateV2EmbeddingQueueStatsInput(input V2EmbeddingQueueStatsInput) error {
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
