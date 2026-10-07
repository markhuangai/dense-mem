package postgres

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/audit/contract"
	"gorm.io/gorm"
)

// InsertAuditEntryTx writes an already prepared entry in the caller's RLS transaction.
func InsertAuditEntryTx(ctx context.Context, tx *gorm.DB, entry contract.Entry) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO audit_log (
			id, team_id, timestamp, operation, entity_type, entity_id,
			before_payload, after_payload, actor_profile_id, actor_role,
			client_ip, correlation_id, metadata, memory_space_id
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13, $14
		)
	`, entry.ID, entry.ProfileID, entry.Timestamp, entry.Operation, entry.EntityType,
		entry.EntityID, entry.BeforePayload, entry.AfterPayload, entry.ActorKeyID,
		entry.ActorRole, entry.ClientIP, entry.CorrelationID, entry.Metadata, entry.MemorySpaceID).Error
}
