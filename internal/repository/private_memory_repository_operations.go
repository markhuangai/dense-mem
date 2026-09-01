package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *PrivateMemoryRepositoryImpl) GetOwnerOperation(ctx context.Context, teamID, operationID uuid.UUID, identityID, credentialID *uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	if teamID == uuid.Nil || operationID == uuid.Nil || (identityID == nil && credentialID == nil) {
		return nil, nil
	}
	var operation *domain.PrivateMemoryErasureOperation
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		query := privateMemoryOperationSelect + ` WHERE id = $1 AND team_id = $2 AND false`
		args := []any{operationID, teamID}
		if identityID != nil {
			query = privateMemoryOperationSelect + `
				WHERE operation.id = $1
				  AND operation.team_id = $2
				  AND (
					EXISTS (
						SELECT 1 FROM memory_spaces AS space
						WHERE space.id = operation.space_id
						  AND space.team_id = operation.team_id
						  AND space.owner_profile_id = $3
					)
					OR EXISTS (
						SELECT 1 FROM credentials AS credential
						WHERE credential.id = operation.target_credential_id
						  AND credential.team_id = operation.team_id
						  AND credential.owner_identity_id = $3
					)
				  )
			`
			args = append(args, *identityID)
		} else if credentialID != nil {
			query = privateMemoryOperationSelect + `
				WHERE operation.id = $1
				  AND operation.team_id = $2
				  AND (
					operation.target_credential_id = $3
					OR EXISTS (
						SELECT 1 FROM memory_spaces AS space
						WHERE space.id = operation.space_id
						  AND space.team_id = operation.team_id
						  AND space.owner_credential_id = $3
					)
				  )
			`
			args = append(args, *credentialID)
		}
		var err error
		operation, err = scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(query, args...).Row())
		if errors.Is(err, sql.ErrNoRows) {
			operation = nil
			return nil
		}
		return err
	})
	return operation, wrapPrivateMemoryError("get owner operation", err)
}

func (r *PrivateMemoryRepositoryImpl) GetOperation(ctx context.Context, operationID uuid.UUID) (*domain.PrivateMemoryErasureOperation, error) {
	var operation *domain.PrivateMemoryErasureOperation
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		operation, err = scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(privateMemoryOperationSelect+` WHERE id = $1`, operationID).Row())
		if errors.Is(err, sql.ErrNoRows) {
			operation = nil
			return nil
		}
		return err
	})
	return operation, wrapPrivateMemoryError("get operation", err)
}

func (r *PrivateMemoryRepositoryImpl) ListOperations(ctx context.Context, limit, offset int) ([]domain.PrivateMemoryErasureOperation, error) {
	limit, offset = privateMemoryPage(limit, offset)
	operations := make([]domain.PrivateMemoryErasureOperation, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(privateMemoryOperationSelect+`
			ORDER BY requested_at DESC, id DESC
			LIMIT $1 OFFSET $2
		`, limit, offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			operation, err := scanPrivateMemoryOperation(rows)
			if err != nil {
				return err
			}
			operations = append(operations, *operation)
		}
		return rows.Err()
	})
	return operations, wrapPrivateMemoryError("list operations", err)
}

func (r *PrivateMemoryRepositoryImpl) ListSpaces(ctx context.Context, limit, offset int) ([]domain.PrivateMemorySpaceMetadata, error) {
	limit, offset = privateMemoryPage(limit, offset)
	items := make([]domain.PrivateMemorySpaceMetadata, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT space.id, space.team_id, space.kind, space.owner_profile_id, space.owner_credential_id,
			       space.generation, space.lifecycle_state, space.private_content_at, space.sealed_at,
			       space.retired_at, space.created_at, space.updated_at,
			       hold.id, hold.reason_code, hold.actor_class, hold.placed_at
			FROM memory_spaces AS space
			LEFT JOIN private_memory_legal_holds AS hold
			  ON hold.space_id = space.id AND hold.released_at IS NULL
			WHERE space.kind IN ('profile_private', 'credential_private')
			ORDER BY space.updated_at DESC, space.id DESC
			LIMIT $1 OFFSET $2
		`, limit, offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			space := domain.MemorySpace{}
			var holdID, holdReason, holdActor sql.NullString
			var holdPlaced sql.NullTime
			if err := rows.Scan(
				&space.ID, &space.TeamID, &space.Kind, &space.OwnerProfileID, &space.OwnerCredentialID,
				&space.Generation, &space.LifecycleState, &space.PrivateContentAt, &space.SealedAt,
				&space.RetiredAt, &space.CreatedAt, &space.UpdatedAt,
				&holdID, &holdReason, &holdActor, &holdPlaced,
			); err != nil {
				return err
			}
			item := domain.PrivateMemorySpaceMetadata{Space: space}
			if holdID.Valid {
				id, err := uuid.Parse(holdID.String)
				if err != nil {
					return err
				}
				item.ActiveHold = &domain.PrivateMemoryLegalHold{
					ID: id, TeamID: space.TeamID, SpaceID: space.ID, ReasonCode: holdReason.String,
					ActorClass: holdActor.String, PlacedAt: holdPlaced.Time.UTC(),
				}
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, wrapPrivateMemoryError("list spaces", err)
}

func (r *PrivateMemoryRepositoryImpl) PlaceLegalHold(ctx context.Context, spaceID uuid.UUID, reasonCode string) (*domain.PrivateMemoryLegalHold, bool, error) {
	reasonCode = strings.TrimSpace(reasonCode)
	if spaceID == uuid.Nil || reasonCode == "" {
		return nil, false, ErrPrivateMemoryNotFound
	}
	var hold *domain.PrivateMemoryLegalHold
	created := false
	now := r.now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		space, err := privateMemorySpaceByIDTx(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		existing, err := activePrivateMemoryLegalHoldTx(ctx, tx, spaceID, true)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.ReasonCode != reasonCode {
				return ErrPrivateMemoryHoldConflict
			}
			hold = existing
			return nil
		}
		hold = &domain.PrivateMemoryLegalHold{
			ID: uuid.New(), TeamID: space.TeamID, SpaceID: space.ID, ReasonCode: reasonCode,
			ActorClass: string(domain.PrivateMemoryActorControl), PlacedAt: now,
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO private_memory_legal_holds (id, team_id, space_id, reason_code, actor_class, placed_at)
			VALUES ($1, $2, $3, $4, 'control', $5)
		`, hold.ID, hold.TeamID, hold.SpaceID, hold.ReasonCode, hold.PlacedAt).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return hold, created, wrapPrivateMemoryError("place legal hold", err)
}

func (r *PrivateMemoryRepositoryImpl) ReleaseLegalHold(ctx context.Context, spaceID uuid.UUID) (*domain.PrivateMemoryLegalHold, bool, error) {
	if spaceID == uuid.Nil {
		return nil, false, ErrPrivateMemoryNotFound
	}
	var hold *domain.PrivateMemoryLegalHold
	released := false
	now := r.now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if _, err := privateMemorySpaceByIDTx(ctx, tx, spaceID); err != nil {
			return err
		}
		var err error
		hold, err = activePrivateMemoryLegalHoldTx(ctx, tx, spaceID, true)
		if err != nil || hold == nil {
			return err
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE private_memory_legal_holds
			SET released_at = $1
			WHERE id = $2 AND released_at IS NULL
		`, now, hold.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			hold.ReleasedAt = &now
			released = true
		}
		return nil
	})
	return hold, released, wrapPrivateMemoryError("release legal hold", err)
}

func privateMemorySpaceByIDTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID) (*domain.MemorySpace, error) {
	space, err := scanPrivateMemorySpace(tx.WithContext(ctx).Raw(`
		SELECT id, team_id, kind, owner_profile_id, owner_credential_id,
		       generation, lifecycle_state, private_content_at, sealed_at, retired_at, created_at, updated_at
		FROM memory_spaces
		WHERE id = $1 AND kind IN ('profile_private', 'credential_private')
		FOR UPDATE
	`, spaceID).Row())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPrivateMemoryNotFound
	}
	return space, err
}

func activePrivateMemoryLegalHoldTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID, lock bool) (*domain.PrivateMemoryLegalHold, error) {
	query := `
		SELECT id, team_id, space_id, reason_code, actor_class, placed_at, released_at
		FROM private_memory_legal_holds
		WHERE space_id = $1 AND released_at IS NULL
	`
	if lock {
		query += " FOR UPDATE"
	}
	hold := &domain.PrivateMemoryLegalHold{}
	err := tx.WithContext(ctx).Raw(query, spaceID).Row().Scan(
		&hold.ID, &hold.TeamID, &hold.SpaceID, &hold.ReasonCode, &hold.ActorClass, &hold.PlacedAt, &hold.ReleasedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return hold, err
}

func ensureNoPrivateMemoryLegalHoldTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID) error {
	hold, err := activePrivateMemoryLegalHoldTx(ctx, tx, spaceID, false)
	if err != nil {
		return err
	}
	if hold != nil {
		return ErrPrivateMemoryLegalHold
	}
	return nil
}

func (r *PrivateMemoryRepositoryImpl) RunRetention(ctx context.Context, input PrivateMemoryRetentionRequest) (*domain.PrivateMemoryRetentionRun, bool, error) {
	if input.RetentionDays <= 0 {
		return nil, false, ErrPrivateMemoryRetentionDisabled
	}
	if input.ActorClass != domain.PrivateMemoryActorControl && input.ActorClass != domain.PrivateMemoryActorRetention {
		return nil, false, fmt.Errorf("%w: invalid retention actor", ErrPrivateMemoryNotFound)
	}
	if len(input.IdempotencyScopeHash) != 64 || len(input.RequestHash) != 64 {
		return nil, false, ErrPrivateMemoryIdempotency
	}
	batchSize := input.BatchSize
	if batchSize <= 0 || batchSize > maximumPrivateRetentionBatch {
		batchSize = defaultPrivateRetentionBatch
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = r.now().UTC()
	}
	cutoff := now.AddDate(0, 0, -input.RetentionDays)
	var run *domain.PrivateMemoryRetentionRun
	created := false
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		if err := lockPrivateMemoryIdempotencyScopeTx(ctx, tx, input.IdempotencyScopeHash); err != nil {
			return err
		}
		var storedRequestHash string
		existing := &domain.PrivateMemoryRetentionRun{}
		err := tx.WithContext(ctx).Raw(`
			SELECT id, actor_class, cutoff, retention_days, queued_count, status, started_at, completed_at, request_hash
			FROM private_memory_retention_runs
			WHERE idempotency_scope_hash = $1
			FOR UPDATE
		`, input.IdempotencyScopeHash).Row().Scan(
			&existing.ID, &existing.ActorClass, &existing.Cutoff, &existing.RetentionDays,
			&existing.QueuedCount, &existing.Status, &existing.StartedAt, &existing.CompletedAt, &storedRequestHash,
		)
		if err == nil {
			if storedRequestHash != input.RequestHash {
				return ErrPrivateMemoryIdempotency
			}
			run = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := backfillPrivateContentAgeTx(ctx, tx, privateContentAgeBackfillBatch); err != nil {
			return err
		}

		rows, err := tx.WithContext(ctx).Raw(`
			SELECT space.id, space.team_id, space.kind, space.owner_profile_id, space.owner_credential_id,
			       space.generation, space.lifecycle_state, space.private_content_at, space.sealed_at,
			       space.retired_at, space.created_at, space.updated_at
			FROM memory_spaces AS space
			WHERE space.kind IN ('profile_private', 'credential_private')
			  AND space.lifecycle_state = 'active'
			  AND space.private_content_at IS NOT NULL
			  AND space.private_content_at < $1
			  AND NOT EXISTS (
				SELECT 1 FROM private_memory_legal_holds AS hold
				WHERE hold.space_id = space.id AND hold.released_at IS NULL
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM private_memory_erasure_operations AS operation
				WHERE operation.space_id = space.id AND operation.status IN ('queued', 'processing')
			  )
			ORDER BY space.private_content_at, space.id
			LIMIT $2
			FOR UPDATE OF space SKIP LOCKED
		`, cutoff, batchSize).Rows()
		if err != nil {
			return err
		}
		spaces := make([]domain.MemorySpace, 0, batchSize)
		for rows.Next() {
			space, err := scanPrivateMemorySpace(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			spaces = append(spaces, *space)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		completedAt := now
		run = &domain.PrivateMemoryRetentionRun{
			ID: uuid.New(), ActorClass: input.ActorClass, Cutoff: cutoff, RetentionDays: input.RetentionDays,
			QueuedCount: len(spaces), Status: "completed", StartedAt: now, CompletedAt: &completedAt,
		}
		if err := tx.WithContext(ctx).Exec(`
			INSERT INTO private_memory_retention_runs (
				id, actor_class, idempotency_scope_hash, request_hash, cutoff, retention_days,
				queued_count, status, started_at, completed_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, 'completed', $8, $8)
		`, run.ID, run.ActorClass, input.IdempotencyScopeHash, input.RequestHash,
			run.Cutoff, run.RetentionDays, run.QueuedCount, now).Error; err != nil {
			return err
		}

		for index := range spaces {
			space := spaces[index]
			scopeHash := privateMemoryHash("retention-operation", run.ID.String(), space.ID.String())
			requestHash := privateMemoryHash(string(domain.PrivateMemoryRetentionPurge), space.ID.String(), fmt.Sprint(space.Generation))
			if _, err := queuePrivateMemorySpaceTx(ctx, tx, &space, queuePrivateMemoryInput{
				Action: domain.PrivateMemoryRetentionPurge, ActorClass: domain.PrivateMemoryActorRetention,
				ReasonCode: "retention_expired", IdempotencyScopeHash: scopeHash, RequestHash: requestHash,
			}); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	return run, created, wrapPrivateMemoryError("run retention", err)
}

func backfillPrivateContentAgeTx(ctx context.Context, tx *gorm.DB, batchSize int) error {
	return tx.WithContext(ctx).Exec(`
		WITH candidate AS (
			SELECT space.id, space.team_id
			FROM memory_spaces AS space
			WHERE space.kind IN ('profile_private', 'credential_private')
			  AND space.private_content_at IS NULL
			  AND EXISTS (
				SELECT 1
				FROM knowledge_ingests AS ingest
				WHERE ingest.team_id = space.team_id AND ingest.space_id = space.id
			  )
			ORDER BY space.id
			LIMIT $1
			FOR UPDATE OF space SKIP LOCKED
		), latest AS (
			SELECT candidate.id, MAX(ingest.created_at) AS private_content_at
			FROM candidate
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = candidate.team_id AND ingest.space_id = candidate.id
			GROUP BY candidate.id
		)
		UPDATE memory_spaces AS space
		SET private_content_at = latest.private_content_at
		FROM latest
		WHERE space.id = latest.id AND space.private_content_at IS NULL
	`, batchSize).Error
}

func (r *PrivateMemoryRepositoryImpl) ListRetentionRuns(ctx context.Context, limit, offset int) ([]domain.PrivateMemoryRetentionRun, error) {
	limit, offset = privateMemoryPage(limit, offset)
	runs := make([]domain.PrivateMemoryRetentionRun, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT id, actor_class, cutoff, retention_days, queued_count, status, started_at, completed_at
			FROM private_memory_retention_runs
			ORDER BY started_at DESC, id DESC
			LIMIT $1 OFFSET $2
		`, limit, offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var run domain.PrivateMemoryRetentionRun
			if err := rows.Scan(&run.ID, &run.ActorClass, &run.Cutoff, &run.RetentionDays,
				&run.QueuedCount, &run.Status, &run.StartedAt, &run.CompletedAt); err != nil {
				return err
			}
			runs = append(runs, run)
		}
		return rows.Err()
	})
	return runs, wrapPrivateMemoryError("list retention runs", err)
}

func (r *PrivateMemoryRepositoryImpl) ClaimNext(ctx context.Context, workerID string, lease time.Duration) (*domain.PrivateMemoryErasureOperation, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, fmt.Errorf("private memory worker ID is required")
	}
	if lease <= 0 {
		lease = defaultPrivateMemoryLease
	}
	now := r.now().UTC()
	leaseUntil := now.Add(lease)
	var operation *domain.PrivateMemoryErasureOperation
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		row := tx.WithContext(ctx).Raw(`
			WITH exhausted AS (
				UPDATE private_memory_erasure_operations
				SET status = 'failed', worker_id = '', lease_until = NULL,
				    next_attempt_at = NULL, last_error_code = 'worker_lease_expired',
				    completed_at = COALESCE(completed_at, $1), updated_at = $1
				WHERE status = 'processing' AND lease_until < $1 AND attempt_count >= $4
				RETURNING id
			), candidate AS (
				SELECT id
				FROM private_memory_erasure_operations
				WHERE ((status = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= $1))
				   OR (status = 'processing' AND lease_until < $1))
				  AND attempt_count < $4
				  AND NOT EXISTS (
					SELECT 1 FROM private_memory_legal_holds AS hold
					WHERE hold.space_id = private_memory_erasure_operations.space_id
					  AND hold.released_at IS NULL
				  )
				ORDER BY requested_at, id
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			), claimed AS (
				UPDATE private_memory_erasure_operations AS operation
				SET status = 'processing',
				    attempt_count = operation.attempt_count + 1,
				    fence = operation.fence + 1,
				    worker_id = $2,
				    lease_until = $3,
				    next_attempt_at = NULL,
				    last_error_code = '',
				    started_at = COALESCE(operation.started_at, $1),
				    updated_at = $1
				FROM candidate
				WHERE operation.id = candidate.id
				RETURNING operation.*
			)
			SELECT id, team_id, space_id::text, space_kind, target_credential_id::text,
			       action, actor_class, reason_code, target_generation, retire_space,
			       status, manifest_position, deleted_counts, attempt_count, fence,
			       worker_id, lease_until, next_attempt_at, last_error_code, requested_at, started_at,
			       completed_at, updated_at
			FROM claimed
		`, now, workerID, leaseUntil, privateMemoryMaximumAttempts).Row()
		var err error
		operation, err = scanPrivateMemoryOperation(row)
		if errors.Is(err, sql.ErrNoRows) {
			operation = nil
			return nil
		}
		return err
	})
	return operation, wrapPrivateMemoryError("claim erasure", err)
}

func (r *PrivateMemoryRepositoryImpl) ExecuteClaim(ctx context.Context, operationID uuid.UUID, workerID string, fence int64) (*domain.PrivateMemoryErasureOperation, error) {
	ordered, err := r.preparedManifest(ctx)
	if err != nil {
		return nil, err
	}
	var completed *domain.PrivateMemoryErasureOperation
	err = r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		operation, err := scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(privateMemoryOperationSelect+`
			WHERE id = $1
		`, operationID).Row())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrPrivateMemoryClaimLost
			}
			return err
		}
		if operation.Status != domain.PrivateMemoryErasureProcessing || operation.WorkerID != workerID || operation.Fence != fence {
			return ErrPrivateMemoryClaimLost
		}
		if operation.SpaceID == nil || operation.SpaceKind == nil || operation.TargetGeneration == nil {
			return ErrPrivateMemoryManifest
		}

		// Match queuePrivateMemorySpaceTx's space-before-operation order to avoid a worker/retirement deadlock.
		space, err := privateMemorySpaceByIDTx(ctx, tx, *operation.SpaceID)
		if err != nil {
			return err
		}
		operation, err = scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(privateMemoryOperationSelect+`
			WHERE id = $1
			FOR UPDATE
		`, operationID).Row())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrPrivateMemoryClaimLost
			}
			return err
		}
		if operation.Status != domain.PrivateMemoryErasureProcessing || operation.WorkerID != workerID || operation.Fence != fence {
			return ErrPrivateMemoryClaimLost
		}
		if operation.SpaceID == nil || operation.SpaceKind == nil || operation.TargetGeneration == nil {
			return ErrPrivateMemoryManifest
		}
		if space.TeamID != operation.TeamID || space.Kind != *operation.SpaceKind ||
			space.LifecycleState != domain.MemorySpaceSealed || space.Generation != *operation.TargetGeneration+1 {
			return ErrPrivateMemoryClaimLost
		}
		if err := ensureNoPrivateMemoryLegalHoldTx(ctx, tx, space.ID); err != nil {
			return err
		}
		if operation.RetireSpace {
			if operation.TargetCredentialID == nil {
				return ErrPrivateMemoryManifest
			}
			var disabled bool
			err := tx.WithContext(ctx).Raw(`
				SELECT status = 'disabled'
				FROM credentials
				WHERE id = $1 AND team_id = $2
			`, *operation.TargetCredentialID, operation.TeamID).Row().Scan(&disabled)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return ErrPrivateMemoryClaimLost
			case err != nil:
				return err
			case !disabled:
				return ErrPrivateMemoryClaimLost
			}
		}

		if err := tx.WithContext(ctx).Exec(`SELECT set_config('app.private_erasure_space_id', $1, true)`, space.ID.String()).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`SET CONSTRAINTS ALL DEFERRED`).Error; err != nil {
			return err
		}
		counts := make(map[string]int64, len(ordered)+1)
		externalDeleted, err := deletePrivateMemoryExternalDependenciesTx(ctx, tx, space.ID)
		if err != nil {
			return err
		}
		counts["v2_migration_corpus_items"] = externalDeleted
		inboundCrossReferencesDeleted, err := deletePrivateMemoryInboundCrossReferencesTx(ctx, tx, space.ID)
		if err != nil {
			return err
		}
		counts["relationship_cross_references_inbound"] = inboundCrossReferencesDeleted
		for _, table := range ordered {
			query, err := privateMemoryManifestDeleteQuery(table)
			if err != nil {
				return err
			}
			result := tx.WithContext(ctx).Exec(query, space.ID)
			if result.Error != nil {
				return fmt.Errorf("delete %s: %w", table, result.Error)
			}
			counts[table] = result.RowsAffected
		}
		auditResult := tx.WithContext(ctx).Exec(`
			UPDATE audit_log
			SET before_payload = NULL,
			    after_payload = NULL,
			    metadata = $1::jsonb
			WHERE memory_space_id = $2
			  AND (before_payload IS NOT NULL OR after_payload IS NOT NULL OR metadata IS DISTINCT FROM $1::jsonb)
		`, privateMemoryAuditMetadataJSON, space.ID)
		if auditResult.Error != nil {
			return fmt.Errorf("scrub audit log: %w", auditResult.Error)
		}
		counts["audit_log"] = auditResult.RowsAffected

		for _, table := range ordered {
			query, err := privateMemoryManifestCountQuery(table)
			if err != nil {
				return err
			}
			var remaining int64
			if err := tx.WithContext(ctx).Raw(query, space.ID).Row().Scan(&remaining); err != nil {
				return fmt.Errorf("verify %s: %w", table, err)
			}
			if remaining != 0 {
				return fmt.Errorf("%w: %s retained %d rows", ErrPrivateMemoryManifest, table, remaining)
			}
		}

		now := r.now().UTC()
		lifecycle := domain.MemorySpaceActive
		var retiredAt any
		var sealedAt any
		if operation.RetireSpace {
			lifecycle = domain.MemorySpaceRetired
			retiredAt = now
			sealedAt = now
		}
		spaceResult := tx.WithContext(ctx).Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = $1,
			    private_content_at = NULL,
			    sealed_at = $2,
			    retired_at = $3,
			    updated_at = $4
			WHERE id = $5
			  AND team_id = $6
			  AND generation = $7
			  AND lifecycle_state = 'sealed'
		`, lifecycle, sealedAt, retiredAt, now, space.ID, space.TeamID, space.Generation)
		if spaceResult.Error != nil {
			return spaceResult.Error
		}
		if spaceResult.RowsAffected != 1 {
			return ErrPrivateMemoryClaimLost
		}
		countsJSON, err := json.Marshal(counts)
		if err != nil {
			return err
		}
		result := tx.WithContext(ctx).Exec(`
			UPDATE private_memory_erasure_operations
			SET status = 'completed',
			    manifest_position = $1,
			    deleted_counts = $2::jsonb,
			    worker_id = '',
			    lease_until = NULL,
			    next_attempt_at = NULL,
			    last_error_code = '',
			    completed_at = $3,
			    updated_at = $3
			WHERE id = $4 AND status = 'processing' AND worker_id = $5 AND fence = $6
		`, len(ordered), string(countsJSON), now, operation.ID, workerID, fence)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPrivateMemoryClaimLost
		}
		if err := tx.WithContext(ctx).Exec(`SELECT set_config('app.private_erasure_space_id', '', true)`).Error; err != nil {
			return err
		}
		completed, err = scanPrivateMemoryOperation(tx.WithContext(ctx).Raw(privateMemoryOperationSelect+` WHERE id = $1`, operation.ID).Row())
		return err
	})
	return completed, wrapPrivateMemoryError("execute erasure", err)
}

func privateMemoryManifestDeleteQuery(table string) (string, error) {
	quoted := pq.QuoteIdentifier(table)
	switch table {
	case "remember_attempt_events", "remember_failure_artifacts":
		return fmt.Sprintf(`
			DELETE FROM %s AS row
			USING remember_attempts AS attempt
			WHERE row.team_id = attempt.team_id
			  AND row.attempt_id = attempt.attempt_id
			  AND row.owner_profile_id = attempt.owner_profile_id
			  AND attempt.space_id = $1`, quoted), nil
	case "semantic_assessments":
		return fmt.Sprintf(`
			DELETE FROM %s AS row
			USING knowledge_ingests AS ingest
			WHERE row.team_id = ingest.team_id
			  AND row.attempt_id = ingest.ingest_id
			  AND row.owner_profile_id = ingest.owner_profile_id
			  AND ingest.space_id = $1`, quoted), nil
	default:
		return fmt.Sprintf("DELETE FROM %s WHERE space_id = $1", quoted), nil
	}
}

func privateMemoryManifestCountQuery(table string) (string, error) {
	quoted := pq.QuoteIdentifier(table)
	switch table {
	case "remember_attempt_events", "remember_failure_artifacts":
		return fmt.Sprintf(`
			SELECT COUNT(*)
			FROM %s AS row
			JOIN remember_attempts AS attempt
			  ON row.team_id = attempt.team_id
			 AND row.attempt_id = attempt.attempt_id
			 AND row.owner_profile_id = attempt.owner_profile_id
			WHERE attempt.space_id = $1`, quoted), nil
	case "semantic_assessments":
		return fmt.Sprintf(`
			SELECT COUNT(*)
			FROM %s AS row
			JOIN knowledge_ingests AS ingest
			  ON row.team_id = ingest.team_id
			 AND row.attempt_id = ingest.ingest_id
			 AND row.owner_profile_id = ingest.owner_profile_id
			WHERE ingest.space_id = $1`, quoted), nil
	default:
		return fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE space_id = $1", quoted), nil
	}
}

func deletePrivateMemoryExternalDependenciesTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID) (int64, error) {
	deleted := int64(0)
	byIngest := tx.WithContext(ctx).Exec(`
		DELETE FROM v2_migration_corpus_items AS corpus
		USING knowledge_ingests AS ingest
		WHERE corpus.team_id = ingest.team_id
		  AND corpus.ingest_id = ingest.ingest_id
		  AND ingest.space_id = $1
	`, spaceID)
	if byIngest.Error != nil {
		return 0, fmt.Errorf("delete v2 migration ingest dependency: %w", byIngest.Error)
	}
	deleted += byIngest.RowsAffected
	var remaining int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM v2_migration_corpus_items AS corpus
		WHERE EXISTS (
			SELECT 1 FROM knowledge_ingests AS ingest
			WHERE ingest.team_id = corpus.team_id
			  AND ingest.ingest_id = corpus.ingest_id
			  AND ingest.space_id = $1
		)
	`, spaceID).Row().Scan(&remaining); err != nil {
		return 0, fmt.Errorf("verify v2 migration dependencies: %w", err)
	}
	if remaining != 0 {
		return 0, fmt.Errorf("%w: v2_migration_corpus_items retained %d rows", ErrPrivateMemoryManifest, remaining)
	}
	return deleted, nil
}

func deletePrivateMemoryInboundCrossReferencesTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID) (int64, error) {
	result := tx.WithContext(ctx).Exec(`
		DELETE FROM relationship_cross_references AS cross_reference
		USING relationship_records AS target_relationship
		WHERE target_relationship.team_id = cross_reference.team_id
		  AND target_relationship.relationship_id = cross_reference.target_relationship_id
		  AND target_relationship.space_id = $1::uuid
		  AND cross_reference.space_id IS DISTINCT FROM $1::uuid
	`, spaceID)
	if result.Error != nil {
		return 0, fmt.Errorf("delete inbound private-memory cross references: %w", result.Error)
	}

	var remaining int64
	if err := tx.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM relationship_cross_references AS cross_reference
		JOIN relationship_records AS target_relationship
		  ON target_relationship.team_id = cross_reference.team_id
		 AND target_relationship.relationship_id = cross_reference.target_relationship_id
		WHERE target_relationship.space_id = $1::uuid
		  AND cross_reference.space_id IS DISTINCT FROM $1::uuid
	`, spaceID).Row().Scan(&remaining); err != nil {
		return 0, fmt.Errorf("verify inbound private-memory cross references: %w", err)
	}
	if remaining != 0 {
		return 0, fmt.Errorf("%w: inbound relationship_cross_references retained %d rows", ErrPrivateMemoryManifest, remaining)
	}
	return result.RowsAffected, nil
}

func (r *PrivateMemoryRepositoryImpl) ReleaseClaim(ctx context.Context, operationID uuid.UUID, workerID string, fence int64, errorCode string) error {
	errorCode = boundedPrivateMemoryErrorCode(errorCode)
	now := r.now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		var attemptCount int
		err := tx.WithContext(ctx).Raw(`
			SELECT attempt_count
			FROM private_memory_erasure_operations
			WHERE id = $1 AND status = 'processing' AND worker_id = $2 AND fence = $3
			FOR UPDATE
		`, operationID, workerID, fence).Row().Scan(&attemptCount)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrivateMemoryClaimLost
		}
		if err != nil {
			return err
		}

		var result *gorm.DB
		if attemptCount >= privateMemoryMaximumAttempts {
			result = tx.WithContext(ctx).Exec(`
				UPDATE private_memory_erasure_operations
				SET status = 'failed', worker_id = '', lease_until = NULL,
				    next_attempt_at = NULL, last_error_code = $1,
				    completed_at = COALESCE(completed_at, $2), updated_at = $2
				WHERE id = $3 AND status = 'processing' AND worker_id = $4 AND fence = $5
			`, errorCode, now, operationID, workerID, fence)
		} else {
			retryAt := now.Add(privateMemoryRetryDelay(attemptCount))
			result = tx.WithContext(ctx).Exec(`
				UPDATE private_memory_erasure_operations
				SET status = 'queued', worker_id = '', lease_until = NULL,
				    next_attempt_at = $1, last_error_code = $2, updated_at = $3
				WHERE id = $4 AND status = 'processing' AND worker_id = $5 AND fence = $6
			`, retryAt, errorCode, now, operationID, workerID, fence)
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrPrivateMemoryClaimLost
		}
		return nil
	})
	return wrapPrivateMemoryError("release erasure claim", err)
}

func privateMemoryRetryDelay(attemptCount int) time.Duration {
	delay := privateMemoryRetryBaseDelay
	for attempt := 1; attempt < attemptCount; attempt++ {
		if delay >= privateMemoryRetryMaximumDelay/2 {
			return privateMemoryRetryMaximumDelay
		}
		delay *= 2
	}
	return delay
}

func (r *PrivateMemoryRepositoryImpl) preparedManifest(ctx context.Context) ([]string, error) {
	r.manifestMu.RLock()
	ordered := append([]string(nil), r.ordered...)
	r.manifestMu.RUnlock()
	if len(ordered) > 0 {
		return ordered, nil
	}
	if err := r.Prepare(ctx); err != nil {
		return nil, err
	}
	r.manifestMu.RLock()
	defer r.manifestMu.RUnlock()
	return append([]string(nil), r.ordered...), nil
}

func validatePrivateMemoryErasureInput(input PrivateMemoryErasureRequest) error {
	if input.TeamID == uuid.Nil || input.OwnerID == uuid.Nil || strings.TrimSpace(input.ReasonCode) == "" {
		return ErrPrivateMemoryNotFound
	}
	if len(input.IdempotencyScopeHash) != 64 || len(input.RequestHash) != 64 {
		return ErrPrivateMemoryIdempotency
	}
	return nil
}

func isPrivateMemorySpaceKind(kind domain.MemorySpaceKind) bool {
	return kind == domain.MemorySpaceProfilePrivate || kind == domain.MemorySpaceCredentialPrivate
}

func privateMemoryPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPrivateMemoryListLimit
	}
	if limit > maximumPrivateMemoryListLimit {
		limit = maximumPrivateMemoryListLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func privateMemoryHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(fmt.Sprintf("%d:", len(part))))
		_, _ = digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func boundedPrivateMemoryErrorCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "database_operation"
	}
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() == 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "database_operation"
	}
	return builder.String()
}

func cloneUUIDPtr(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func wrapPrivateMemoryError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if !isPrivateMemoryBoundedError(err) {
		err = ErrPrivateMemoryInternal
	}
	return fmt.Errorf("private memory %s: %w", operation, err)
}

func isPrivateMemoryBoundedError(err error) bool {
	for _, known := range []error{
		ErrPrivateMemoryNotFound,
		ErrPrivateMemoryLegalHold,
		ErrPrivateMemoryIdempotency,
		ErrPrivateMemoryOperationConflict,
		ErrPrivateMemoryManifest,
		ErrPrivateMemoryClaimLost,
		ErrPrivateMemoryRetentionDisabled,
		ErrPrivateMemoryHoldConflict,
		context.Canceled,
		context.DeadlineExceeded,
	} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}
