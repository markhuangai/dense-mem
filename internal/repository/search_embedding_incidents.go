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
	if err := lockEmbeddingFailureIncident(ctx, tx, teamID, sourceKind, contractID, failureClass, failureCode, dimensions); err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE embedding_failure_incidents
		SET status = 'open', affected_job_count = (
			SELECT count(*) FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ? AND source_kind = ?
			  AND failure_class = ? AND failure_code = ?
			  AND first_failed_at IS NOT NULL
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
				   AND first_failed_at IS NOT NULL
				   AND status IN ('queued', 'processing', 'failed')),
			now(), now(), now()
		)
		ON CONFLICT (team_id, embedding_contract_id, embedding_dimensions, source_kind, failure_class, failure_code, status)
		DO UPDATE SET affected_job_count = EXCLUDED.affected_job_count,
		              last_seen_at = now(), updated_at = now()
	`, teamID, contractID, dimensions, sourceKind, failureClass, failureCode,
		teamID, contractID, dimensions, sourceKind, failureClass, failureCode).Error
}
