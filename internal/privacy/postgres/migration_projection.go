package postgres

import (
	"context"
	"gorm.io/gorm"
)

func (r *PrivateMemoryRepositoryImpl) rebuildPrivateMigrationBatch(ctx context.Context) (int, error) {
	count := int64(0)
	err := r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE memory_spaces space SET generation=dest.generation FROM (SELECT destination_space_id,max(destination_generation) generation FROM private_memory_migration_records GROUP BY destination_space_id) dest WHERE space.id=dest.destination_space_id AND EXISTS(SELECT 1 FROM private_memory_migration_state WHERE singleton AND phase<>'rebuilding')`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE private_memory_migration_state SET phase='rebuilding',updated_at=now() WHERE singleton`).Error; err != nil {
			return err
		}
		result := tx.Exec(`UPDATE private_memory_migration_records SET projection_rebuilt=true WHERE record_id IN (SELECT record_id FROM private_memory_migration_records WHERE completed AND NOT projection_rebuilt ORDER BY record_id LIMIT ? FOR UPDATE)`, privateMigrationBatchSize)
		count = result.RowsAffected
		return result.Error
	})
	return int(count), err
}
