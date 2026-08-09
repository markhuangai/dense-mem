package repository

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

func upsertEmbeddingFailureIncident(
	ctx context.Context,
	tx *gorm.DB,
	teamID, failureClass, failureCode, contractID string,
	dimensions int,
	sourceKind string,
) error {
	if status := strings.TrimSpace(failureClass); status == "" {
		return errors.New("embedding failure class is required")
	}
	if strings.TrimSpace(failureCode) == "" {
		return errors.New("embedding failure code is required")
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE embedding_failure_incidents
		SET status = 'open', affected_job_count = (
			SELECT count(*) FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ? AND source_kind = ?
			  AND failure_class = ? AND failure_code = ?
			  AND status IN ('queued', 'processing', 'failed')
		), last_seen_at = now(), resolved_at = NULL, recovering_at = NULL,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND embedding_contract_id = ?::uuid
		  AND embedding_dimensions = ?
		  AND source_kind = ?
		  AND failure_class = ?
		  AND failure_code = ?
		  AND status IN ('open', 'recovering', 'resolved')
	`, teamID, contractID, dimensions, sourceKind, failureClass, failureCode,
		teamID, contractID, dimensions, sourceKind, failureClass, failureCode)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO embedding_failure_incidents (
			team_id, embedding_contract_id, embedding_dimensions, source_kind,
			failure_class, failure_code, status, affected_job_count,
			first_seen_at, last_seen_at, updated_at
		) VALUES (
			?::uuid, ?::uuid, ?, ?, ?, ?, 'open',
			(SELECT count(*) FROM embedding_jobs
			 WHERE team_id = ?::uuid AND embedding_contract_id = ?::uuid
			   AND embedding_dimensions = ? AND source_kind = ?
			   AND failure_class = ? AND failure_code = ?
			   AND status IN ('queued', 'processing', 'failed')),
			now(), now(), now()
		)
		ON CONFLICT (team_id, embedding_contract_id, embedding_dimensions, source_kind, failure_class, failure_code, status)
		DO UPDATE SET affected_job_count = EXCLUDED.affected_job_count,
		              last_seen_at = now(), updated_at = now()
	`, teamID, contractID, dimensions, sourceKind, failureClass, failureCode,
		teamID, contractID, dimensions, sourceKind, failureClass, failureCode).Error
}

func resolveEmbeddingIncidentsForJob(ctx context.Context, tx *gorm.DB, teamID, sourceKind, contractID string, dimensions int) error {
	return tx.WithContext(ctx).Exec(`
		WITH remaining AS (
			SELECT incident.team_id, incident.incident_id,
			       count(job.embedding_job_id) AS affected_job_count
			FROM embedding_failure_incidents AS incident
			LEFT JOIN embedding_jobs AS job
			  ON job.team_id = incident.team_id
			 AND job.embedding_contract_id = incident.embedding_contract_id
			 AND job.embedding_dimensions = incident.embedding_dimensions
			 AND job.source_kind = incident.source_kind
			 AND job.failure_class = incident.failure_class
			 AND job.failure_code = incident.failure_code
			 AND job.status IN ('queued', 'processing', 'failed')
			WHERE incident.team_id = ?::uuid
			  AND incident.embedding_contract_id = ?::uuid
			  AND incident.embedding_dimensions = ?
			  AND incident.source_kind = ?
			  AND incident.status IN ('open', 'recovering')
			GROUP BY incident.team_id, incident.incident_id
		)
		UPDATE embedding_failure_incidents AS incident
		SET status = CASE WHEN remaining.affected_job_count = 0 THEN 'resolved' ELSE incident.status END,
		    resolved_at = CASE WHEN remaining.affected_job_count = 0 THEN now() ELSE NULL END,
		    affected_job_count = remaining.affected_job_count,
		    updated_at = now()
		FROM remaining
		WHERE incident.team_id = remaining.team_id
		  AND incident.incident_id = remaining.incident_id
	`, teamID, contractID, dimensions, sourceKind).Error
}
