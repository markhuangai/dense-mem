package postgres

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SetRememberAttemptDiagnosticHoldStateTx updates diagnostic retention in the
// caller's transaction after the caller has established the hold state.
func SetRememberAttemptDiagnosticHoldStateTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID, retained bool) error {
	if spaceID == uuid.Nil {
		return nil
	}
	if err := tx.WithContext(ctx).Exec(
		"SELECT set_config('app.remember_attempt_diagnostic_retention_space_id', ?, true)",
		spaceID.String(),
	).Error; err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE remember_attempt_diagnostics AS diagnostic
		SET retained_by_legal_hold = ?
		FROM remember_attempts AS attempt
		WHERE attempt.team_id = diagnostic.team_id
		  AND attempt.attempt_id = diagnostic.attempt_id
		  AND attempt.owner_profile_id = diagnostic.owner_profile_id
		  AND attempt.space_id = ?::uuid
		  AND diagnostic.retained_by_legal_hold IS DISTINCT FROM ?
	`, retained, spaceID, retained)
	if result.Error != nil {
		return result.Error
	}
	return tx.WithContext(ctx).Exec(
		"SELECT set_config('app.remember_attempt_diagnostic_retention_space_id', '', true)",
	).Error
}
