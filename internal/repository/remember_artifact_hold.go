package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (r *LedgerRepositoryImpl) synchronizeRememberFailureArtifactHold(ctx context.Context, rawSpaceID string) error {
	spaceID, err := uuid.Parse(rawSpaceID)
	if err != nil {
		return fmt.Errorf("space_id is invalid: %w", err)
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var lockedSpace uuid.UUID
		if err := tx.WithContext(ctx).Raw(`
			SELECT id
			FROM memory_spaces
			WHERE id = $1 AND kind IN ('profile_private', 'credential_private')
			FOR UPDATE
		`, spaceID).Row().Scan(&lockedSpace); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		var held bool
		if err := tx.WithContext(ctx).Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM private_memory_legal_holds
				WHERE space_id = $1 AND released_at IS NULL
			)
		`, lockedSpace).Row().Scan(&held); err != nil {
			return err
		}
		return setRememberFailureArtifactHoldStateTx(ctx, tx, lockedSpace, held)
	})
}

func setRememberFailureArtifactHoldStateTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID, retained bool) error {
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
