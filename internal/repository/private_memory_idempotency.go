package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func existingPrivateMemoryOperationTx(ctx context.Context, tx *gorm.DB, scopeHash, requestHash string) (*domain.PrivateMemoryErasureOperation, error) {
	operation, err := scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(privateMemoryOperationSelect+`
			JOIN private_memory_erasure_idempotency_keys AS idempotency
			  ON idempotency.operation_id = operation.id
			WHERE idempotency.idempotency_scope_hash = $1
		`, scopeHash).Row())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var storedRequestHash string
	if err := tx.WithContext(ctx).Raw(`
		SELECT request_hash
		FROM private_memory_erasure_idempotency_keys
		WHERE idempotency_scope_hash = $1 AND operation_id = $2
	`, scopeHash, operation.ID).Row().Scan(&storedRequestHash); err != nil {
		return nil, err
	}
	if storedRequestHash != requestHash {
		return nil, ErrPrivateMemoryIdempotency
	}
	return operation, nil
}

func lockPrivateMemoryIdempotencyScopeTx(ctx context.Context, tx *gorm.DB, scopeHash string) error {
	if strings.TrimSpace(scopeHash) == "" {
		return ErrPrivateMemoryIdempotency
	}
	return tx.WithContext(ctx).Exec(`
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, scopeHash).Error
}

func insertPrivateMemoryOperationTx(ctx context.Context, tx *gorm.DB, operation *domain.PrivateMemoryErasureOperation, scopeHash, requestHash string) error {
	deletedCounts, err := json.Marshal(operation.DeletedCounts)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO private_memory_erasure_operations (
			id, team_id, space_id, space_kind, target_credential_id, action, actor_class,
			reason_code, target_generation, retire_space, idempotency_scope_hash, request_hash,
			status, manifest_position, deleted_counts, attempt_count, fence, worker_id,
			lease_until, next_attempt_at, last_error_code, requested_at, started_at, completed_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15::jsonb, $16, $17, $18,
			$19, $20, $21, $22, $23, $24, $25
		)
	`, operation.ID, operation.TeamID, operation.SpaceID, operation.SpaceKind, operation.TargetCredentialID,
		operation.Action, operation.ActorClass, operation.ReasonCode, operation.TargetGeneration, operation.RetireSpace,
		scopeHash, requestHash, operation.Status, operation.ManifestPosition, string(deletedCounts), operation.AttemptCount,
		operation.Fence, operation.WorkerID, operation.LeaseUntil, operation.NextAttemptAt, operation.LastErrorCode, operation.RequestedAt,
		operation.StartedAt, operation.CompletedAt, operation.UpdatedAt).Error; err != nil {
		return err
	}
	return insertPrivateMemoryIdempotencyKeyTx(ctx, tx, scopeHash, requestHash, operation.ID)
}

func insertPrivateMemoryIdempotencyKeyTx(ctx context.Context, tx *gorm.DB, scopeHash, requestHash string, operationID uuid.UUID) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO private_memory_erasure_idempotency_keys (
			idempotency_scope_hash, request_hash, operation_id
		) VALUES ($1, $2, $3)
	`, scopeHash, requestHash, operationID).Error
}
