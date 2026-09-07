package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
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
	return storagepostgres.SetRememberFailureArtifactHoldStateTx(ctx, tx, spaceID, retained)
}
