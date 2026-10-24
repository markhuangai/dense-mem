package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func (s *Store) AdmitMaintenanceAttempt(ctx context.Context, claim ontology.MaintenanceClaim, assessmentID string, number, input, output int, now time.Time) error {
	if _, err := uuid.Parse(assessmentID); err != nil || number < 1 || number > 3 || input < 1 || output < 1 {
		return ontology.ErrInvalid
	}
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		paused, err := maintenanceState(tx)
		if err != nil {
			return err
		}
		if paused {
			return ontology.ErrMaintenancePaused
		}
		enabled, err := maintenanceEnabled(tx)
		if err != nil {
			return err
		}
		if !enabled {
			return ontology.ErrMaintenanceDisabled
		}
		window, err := scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM ontology_maintenance_windows WHERE window_id=?::uuid FOR UPDATE`, claim.WindowID).Row())
		if err != nil {
			return err
		}
		if window == nil || now.Before(window.StartsAt) || !now.Before(window.EndsAt) || window.Policy.ContextWindowTokens == 0 {
			return ontology.ErrWindowClosed
		}
		if err := maintenancePublicationFence(ontology.WithMaintenanceClaim(ctx, claim), tx, scope{TeamID: claim.TeamID, SpaceID: claim.SpaceID, Generation: claim.Generation}); err != nil {
			return err
		}
		var reservedInput, reservedOutput int
		err = tx.Raw(`SELECT estimated_input_tokens,max_output_tokens FROM ontology_maintenance_attempts WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=?`, claim.ID, assessmentID, number).Row().Scan(&reservedInput, &reservedOutput)
		if err == nil {
			if reservedInput != input || reservedOutput != output {
				return ontology.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if int64(input)+int64(output) > window.Policy.ContextWindowTokens {
			return ontology.ErrConfiguration
		}
		return tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,estimated_input_tokens,max_output_tokens,admitted_at)
		 VALUES(?::uuid,?::uuid,?,?::uuid,0,0,?,?,?)`, claim.ID, assessmentID, number, claim.WindowID, input, output, now).Error
	})
}

func (s *Store) ReconcileMaintenanceAttempt(ctx context.Context, claim ontology.MaintenanceClaim, assessmentID string, attempt ontology.AssessmentAttempt) error {
	if attempt.Number < 1 || attempt.Number > 3 || attempt.ReportedInputTokens < 0 || attempt.ReportedOutputTokens < 0 || attempt.ReportedTotalTokens < 0 {
		return ontology.ErrInvalid
	}
	return s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		if _, err := maintenanceState(tx); err != nil {
			return err
		}
		window, err := scanMaintenanceWindow(tx.Raw(`SELECT `+maintenanceWindowColumns+` FROM ontology_maintenance_windows WHERE window_id=?::uuid FOR UPDATE`, claim.WindowID).Row())
		if err != nil {
			return err
		}
		if window == nil {
			return ontology.ErrAccounting
		}
		var reportedInput, reportedOutput, reportedTotal sql.NullInt64
		var reconciled sql.NullTime
		err = tx.Raw(`SELECT reported_input,reported_output,reported_total,reconciled_at FROM ontology_maintenance_attempts WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=? AND window_id=?::uuid FOR UPDATE`, claim.ID, assessmentID, attempt.Number, claim.WindowID).Row().Scan(&reportedInput, &reportedOutput, &reportedTotal, &reconciled)
		if err != nil {
			return err
		}
		if reconciled.Valid {
			if reportedInput.Int64 != int64(attempt.ReportedInputTokens) || reportedOutput.Int64 != int64(attempt.ReportedOutputTokens) || reportedTotal.Int64 != int64(attempt.ReportedTotalTokens) {
				return ontology.ErrConflict
			}
			return nil
		}
		return tx.Exec(`UPDATE ontology_maintenance_attempts SET reported_input=?,reported_output=?,reported_total=?,reconciled_at=clock_timestamp()
		 WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=? AND window_id=?::uuid`, attempt.ReportedInputTokens, attempt.ReportedOutputTokens, attempt.ReportedTotalTokens, claim.ID, assessmentID, attempt.Number, claim.WindowID).Error
	})
}
