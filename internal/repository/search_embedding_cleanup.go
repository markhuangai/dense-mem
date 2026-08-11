package repository

import (
	"context"

	"gorm.io/gorm"
)

func failExpiredMaxAttemptEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string, limit int) error {
	return tx.WithContext(ctx).Exec(`
		WITH document_candidates AS MATERIALIZED (
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
			  AND job.status = 'processing'
			  AND job.lease_until <= clock_timestamp()
			  AND job.attempts >= job.max_attempts
			ORDER BY job.lease_until ASC, job.embedding_job_id ASC
			LIMIT ?
			FOR UPDATE OF document SKIP LOCKED
		), exhausted AS MATERIALIZED (
			SELECT job.team_id, job.embedding_job_id
			FROM document_candidates AS candidate
			JOIN embedding_jobs AS job
			  ON job.team_id = candidate.team_id
			 AND job.embedding_job_id = candidate.embedding_job_id
			WHERE job.status = 'processing'
			  AND job.lease_until <= clock_timestamp()
			  AND job.attempts >= job.max_attempts
			ORDER BY job.lease_until ASC, job.embedding_job_id ASC
			FOR UPDATE OF job SKIP LOCKED
		), failed AS (
			UPDATE embedding_jobs AS job
			SET status = 'failed', error = ?,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = COALESCE(first_failed_at, now()),
			    last_failed_at = now(), completed_at = now(),
			    lease_until = NULL, worker_id = '', updated_at = now()
			FROM exhausted
			WHERE job.team_id = exhausted.team_id
			  AND job.embedding_job_id = exhausted.embedding_job_id
			RETURNING job.team_id, job.search_document_id, job.source_version,
			          job.projection_format_version, job.projection_generation_id,
			          job.document_version, job.embedding_contract_id,
			          job.embedding_dimensions
		)
		UPDATE search_documents AS document
		SET search_state = 'failed', embedding_error = ?, updated_at = now()
		FROM failed
		WHERE document.team_id = failed.team_id
		  AND document.search_document_id = failed.search_document_id
		  AND document.source_version = failed.source_version
		  AND document.projection_format_version = failed.projection_format_version
		  AND document.projection_generation_id IS NOT DISTINCT FROM failed.projection_generation_id
		  AND document.document_version = failed.document_version
		  AND document.embedding_contract_id = failed.embedding_contract_id
		  AND document.embedding_dimensions = failed.embedding_dimensions
	`, teamID, limit, embeddingJobAttemptsExhaustedMessage, embeddingJobAttemptsExhaustedMessage).Error
}
