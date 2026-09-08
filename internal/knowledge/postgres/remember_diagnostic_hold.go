package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func (r *Store) synchronizeRememberAttemptDiagnosticHold(ctx context.Context, rawSpaceID string) error {
	spaceID, err := uuid.Parse(rawSpaceID)
	if err != nil {
		return fmt.Errorf("space_id is invalid: %w", err)
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var held bool
		if err := tx.WithContext(ctx).Raw(`
			SELECT EXISTS (
				SELECT 1 FROM private_memory_legal_holds
				WHERE space_id = $1 AND released_at IS NULL
			)
		`, spaceID).Row().Scan(&held); err != nil {
			return err
		}
		return storagepostgres.SetRememberAttemptDiagnosticHoldStateTx(ctx, tx, spaceID, held)
	})
}
