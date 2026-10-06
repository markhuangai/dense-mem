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

func (s *Store) ReserveMaintenanceAttempt(ctx context.Context, claim ontology.MaintenanceClaim, assessmentID string, number, input, output int, now time.Time) error {
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
		if window == nil || now.Before(window.StartsAt) || !now.Before(window.EndsAt) || window.Overrun {
			return ontology.ErrBudgetDeferred
		}
		if err := maintenancePublicationFence(ontology.WithMaintenanceClaim(ctx, claim), tx, scope{TeamID: claim.TeamID, SpaceID: claim.SpaceID, Generation: claim.Generation}); err != nil {
			return err
		}
		var reservedInput, reservedOutput int
		err = tx.Raw(`SELECT reserved_input,reserved_output FROM ontology_maintenance_attempts WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=?`, claim.ID, assessmentID, number).Row().Scan(&reservedInput, &reservedOutput)
		if err == nil {
			if reservedInput != input || reservedOutput != output {
				return ontology.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if int64(input) > window.Policy.InputTokens-window.ChargedInput || int64(output) > window.Policy.OutputTokens-window.ChargedOutput {
			return ontology.ErrBudgetDeferred
		}
		if err := tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,admitted_at) VALUES(?::uuid,?::uuid,?,?::uuid,?,?,?)`, claim.ID, assessmentID, number, claim.WindowID, input, output, now).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE ontology_maintenance_windows SET charged_input=charged_input+?,charged_output=charged_output+?,reserved_input=reserved_input+?,reserved_output=reserved_output+? WHERE window_id=?::uuid`, input, output, input, output, claim.WindowID).Error
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
		var input, output int64
		var reportedInput, reportedOutput, reportedTotal sql.NullInt64
		var reconciled sql.NullTime
		err = tx.Raw(`SELECT reserved_input,reserved_output,reported_input,reported_output,reported_total,reconciled_at FROM ontology_maintenance_attempts WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=? AND window_id=?::uuid FOR UPDATE`, claim.ID, assessmentID, attempt.Number, claim.WindowID).Row().Scan(&input, &output, &reportedInput, &reportedOutput, &reportedTotal, &reconciled)
		if err != nil {
			return err
		}
		if reconciled.Valid {
			if reportedInput.Int64 != int64(attempt.ReportedInputTokens) || reportedOutput.Int64 != int64(attempt.ReportedOutputTokens) || reportedTotal.Int64 != int64(attempt.ReportedTotalTokens) {
				return ontology.ErrConflict
			}
			return nil
		}
		chargedInput, chargedOutput := input, output
		releaseInput, releaseOutput := int64(0), int64(0)
		if attempt.ReportedInputTokens > 0 {
			chargedInput = int64(attempt.ReportedInputTokens)
			releaseInput = input
		}
		if attempt.ReportedOutputTokens > 0 {
			chargedOutput = int64(attempt.ReportedOutputTokens)
			releaseOutput = output
		}
		if total := int64(attempt.ReportedTotalTokens); total > chargedInput+chargedOutput {
			chargedOutput = total - chargedInput
		}
		overrun := chargedInput > input || chargedOutput > output || window.ChargedInput-input+chargedInput > window.Policy.InputTokens || window.ChargedOutput-output+chargedOutput > window.Policy.OutputTokens
		if err := tx.Exec(`UPDATE ontology_maintenance_attempts SET reported_input=?,reported_output=?,reported_total=?,reconciled_at=clock_timestamp() WHERE batch_id=?::uuid AND assessment_id=?::uuid AND attempt=?`, attempt.ReportedInputTokens, attempt.ReportedOutputTokens, attempt.ReportedTotalTokens, claim.ID, assessmentID, attempt.Number).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE ontology_maintenance_windows SET charged_input=charged_input+?,charged_output=charged_output+?,reported_input=reported_input+?,reported_output=reported_output+?,reserved_input=reserved_input-?,reserved_output=reserved_output-?,overrun=overrun OR ? WHERE window_id=?::uuid`, chargedInput-input, chargedOutput-output, attempt.ReportedInputTokens, attempt.ReportedOutputTokens, releaseInput, releaseOutput, overrun, claim.WindowID).Error
	})
}
