package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func resolveEmbeddingIncidentsWithoutActiveJobs(ctx context.Context, tx *gorm.DB, teamID string) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE embedding_failure_incidents AS incident
		SET status = 'resolved', resolved_at = now(), affected_job_count = 0,
		    updated_at = now()
		WHERE incident.team_id = ?::uuid
		  AND incident.status IN ('open', 'recovering')
		  AND NOT EXISTS (
			SELECT 1 FROM embedding_jobs AS job
			WHERE job.team_id = incident.team_id
			  AND job.embedding_contract_id = incident.embedding_contract_id
			  AND job.embedding_dimensions = incident.embedding_dimensions
			  AND job.source_kind = incident.source_kind
			  AND job.failure_class = incident.failure_class
			  AND job.failure_code = incident.failure_code
			  AND job.status IN ('queued', 'processing', 'failed')
		  )
	`, teamID).Error
}

func failExpiredMaxAttemptEmbeddingJobs(ctx context.Context, tx *gorm.DB, teamID string, limit int) error {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH exhausted AS MATERIALIZED (
			SELECT job.team_id, job.embedding_job_id
			FROM embedding_jobs AS job
			WHERE job.team_id = ?::uuid
			  AND job.status = 'processing'
			  AND job.lease_until <= clock_timestamp()
			  AND job.attempts >= job.max_attempts
			ORDER BY job.lease_until ASC, job.embedding_job_id ASC
			LIMIT ?
			FOR UPDATE SKIP LOCKED
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
			          job.embedding_dimensions, job.source_kind
		), documents AS (
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
			RETURNING 1
		)
		SELECT embedding_contract_id::text, embedding_dimensions, source_kind
		FROM failed
	`, teamID, limit, embeddingJobAttemptsExhaustedMessage, embeddingJobAttemptsExhaustedMessage).Rows()
	if err != nil {
		return err
	}
	incidentKeys := make(map[struct {
		contractID string
		dimensions int
		sourceKind string
	}]struct{})
	for rows.Next() {
		var contractID, sourceKind string
		var dimensions int
		if err := rows.Scan(&contractID, &dimensions, &sourceKind); err != nil {
			_ = rows.Close()
			return err
		}
		incidentKeys[struct {
			contractID string
			dimensions int
			sourceKind string
		}{contractID: contractID, dimensions: dimensions, sourceKind: sourceKind}] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for key := range incidentKeys {
		if err := upsertEmbeddingFailureIncident(ctx, tx, teamID,
			string(domain.EmbeddingFailureTransient), string(domain.EmbeddingFailureProviderTimeout),
			key.contractID, key.dimensions, key.sourceKind); err != nil {
			return err
		}
	}
	return nil
}
