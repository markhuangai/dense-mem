package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SetRememberFailureArtifactHoldStateTx updates failure-artifact retention in
// the caller's transaction after the caller has established the hold state.
func SetRememberFailureArtifactHoldStateTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID, retained bool) error {
	if spaceID == uuid.Nil {
		return nil
	}
	if err := tx.WithContext(ctx).Exec(
		"SELECT set_config('app.remember_failure_artifact_retention_space_id', ?, true), set_config('app.remember_failure_artifact_retention_value', ?, true)",
		spaceID.String(), fmt.Sprint(retained),
	).Error; err != nil {
		return err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE remember_failure_artifacts AS artifact
		SET retained_by_legal_hold = ?
		FROM remember_attempts AS attempt
		WHERE attempt.team_id = artifact.team_id
		  AND attempt.attempt_id = artifact.attempt_id
		  AND attempt.owner_profile_id = artifact.owner_profile_id
		  AND attempt.space_id = ?::uuid
		  AND artifact.retained_by_legal_hold IS DISTINCT FROM ?
	`, retained, spaceID, retained)
	if result.Error != nil {
		return result.Error
	}
	return tx.WithContext(ctx).Exec(
		"SELECT set_config('app.remember_failure_artifact_retention_space_id', '', true), set_config('app.remember_failure_artifact_retention_value', '', true)",
	).Error
}
