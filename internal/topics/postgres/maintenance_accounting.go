package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) AdmitMaintenanceAttempt(ctx context.Context, claim topic.MaintenanceClaim, assessmentID string, number, input, output int, now time.Time) error {
	if _, err := uuid.Parse(assessmentID); err != nil || number < 1 || number > 3 || input < 1 || output < 1 {
		return topic.ErrInvalid
	}
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		paused, err := maintenanceStateShared(tx)
		if err != nil {
			return err
		}
		if paused {
			return topic.ErrMaintenancePaused
		}
		enabled, err := maintenanceEnabled(tx)
		if err != nil {
			return err
		}
		if !enabled {
			return topic.ErrMaintenanceDisabled
		}
		window, err := scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM topic_maintenance_windows WHERE window_id=?::uuid`, claim.WindowID).Row())
		if err != nil {
			return err
		}
		if window == nil || now.Before(window.StartsAt) || !now.Before(window.EndsAt) || window.Policy.ContextWindowTokens == 0 {
			return topic.ErrWindowClosed
		}
		if err := maintenancePublicationFence(topic.WithMaintenanceClaim(ctx, claim), tx, scope{TeamID: claim.TeamID, SpaceID: claim.SpaceID, Generation: claim.Generation}); err != nil {
			return err
		}
		var reservedInput, reservedOutput int
		err = tx.Raw(`SELECT estimated_input_tokens,max_output_tokens FROM topic_maintenance_attempts WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=?`, claim.ID, assessmentID, number).Row().Scan(&reservedInput, &reservedOutput)
		if err == nil {
			if reservedInput != input || reservedOutput != output {
				return topic.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if claim.DecisionMaxInputTokens > 0 && int64(input) > claim.DecisionMaxInputTokens ||
			claim.DecisionMaxInputTokens == 0 && int64(input)+int64(output) > window.Policy.ContextWindowTokens {
			return topic.ErrConfiguration
		}
		renewedUntil := now.Add(claim.AttemptLease)
		if err := tx.Exec(`WITH recorded AS (
			INSERT INTO topic_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,estimated_input_tokens,max_output_tokens,admitted_at)
			VALUES(?::uuid,?::uuid,?,?::uuid,0,0,?,?,?) RETURNING batch_id
		)
		UPDATE topic_maintenance_batches SET lease_until=? WHERE ? AND lease_until<? AND batch_id IN(SELECT batch_id FROM recorded)`,
			claim.ID, assessmentID, number, claim.WindowID, input, output, now, renewedUntil, claim.AttemptLease > 0, renewedUntil).Error; err != nil {
			return err
		}
		if claim.Kind == "community" && claim.AttemptLease > 0 {
			return tx.Exec(`UPDATE topic_maintenance_community_work AS work SET lease_until=batch.lease_until
				FROM topic_maintenance_batches AS batch WHERE batch.batch_id=?::uuid AND work.lease_batch_id=batch.batch_id`, claim.ID).Error
		}
		if claim.Kind == "consolidation" && claim.AttemptLease > 0 {
			return tx.Exec(`UPDATE topic_embeddings AS embedding SET consolidation_lease_until=batch.lease_until
				FROM topic_maintenance_batches AS batch WHERE batch.batch_id=?::uuid AND embedding.consolidation_batch_id=batch.batch_id`, claim.ID).Error
		}
		return nil
	})
}

func (s *Store) ReconcileMaintenanceAttempt(ctx context.Context, claim topic.MaintenanceClaim, assessmentID string, attempt topic.AssessmentAttempt) error {
	if attempt.Number < 1 || attempt.Number > 3 || attempt.ReportedInputTokens < 0 || attempt.ReportedOutputTokens < 0 || attempt.ReportedTotalTokens < 0 {
		return topic.ErrInvalid
	}
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		var windowExists bool
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM topic_maintenance_windows WHERE window_id=?::uuid)`, claim.WindowID).Row().Scan(&windowExists); err != nil {
			return err
		}
		if !windowExists {
			return topic.ErrAccounting
		}
		var reportedInput, reportedOutput, reportedTotal sql.NullInt64
		var reconciled sql.NullTime
		err := tx.Raw(`SELECT reported_input,reported_output,reported_total,reconciled_at FROM topic_maintenance_attempts WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=? AND window_id=?::uuid FOR UPDATE`, claim.ID, assessmentID, attempt.Number, claim.WindowID).Row().Scan(&reportedInput, &reportedOutput, &reportedTotal, &reconciled)
		if err != nil {
			return err
		}
		if reconciled.Valid {
			if reportedInput.Int64 != int64(attempt.ReportedInputTokens) || reportedOutput.Int64 != int64(attempt.ReportedOutputTokens) || reportedTotal.Int64 != int64(attempt.ReportedTotalTokens) {
				return topic.ErrConflict
			}
			return nil
		}
		return tx.Exec(`UPDATE topic_maintenance_attempts SET reported_input=?,reported_output=?,reported_total=?,reconciled_at=clock_timestamp()
		 WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=? AND window_id=?::uuid`, attempt.ReportedInputTokens, attempt.ReportedOutputTokens, attempt.ReportedTotalTokens, claim.ID, assessmentID, attempt.Number, claim.WindowID).Error
	})
}
