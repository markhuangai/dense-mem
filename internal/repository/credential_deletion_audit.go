package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// CredentialDeletionAuditInput carries actor metadata for the atomic
// credential-delete and audit transaction. The repository owns the payload
// so callers cannot accidentally persist credential material.
type CredentialDeletionAuditInput struct {
	ActorCredentialID *string
	ActorRole         string
	ClientIP          string
	CorrelationID     string
}

// CredentialDeletionAuditRepository is an optional extension implemented by
// the production repository. Keeping it separate preserves lightweight auth
// fakes and the existing credential repository contract.
type CredentialDeletionAuditRepository interface {
	DeleteForTeamWithAudit(ctx context.Context, teamID, id uuid.UUID, input CredentialDeletionAuditInput) (int64, error)
}

var _ CredentialDeletionAuditRepository = (*CredentialRepositoryImpl)(nil)

// DeleteForTeam removes an API credential from supported reads while retaining its stable audit identity.
func (r *CredentialRepositoryImpl) DeleteForTeam(ctx context.Context, teamID, id uuid.UUID) (int64, error) {
	return r.deleteForTeam(ctx, teamID, id, nil)
}

// DeleteForTeamWithAudit deletes a credential and appends its deletion audit
// before a private-memory erasure job becomes visible to workers.
func (r *CredentialRepositoryImpl) DeleteForTeamWithAudit(
	ctx context.Context,
	teamID, id uuid.UUID,
	input CredentialDeletionAuditInput,
) (int64, error) {
	return r.deleteForTeam(ctx, teamID, id, &input)
}

func (r *CredentialRepositoryImpl) deleteForTeam(
	ctx context.Context,
	teamID, id uuid.UUID,
	auditInput *CredentialDeletionAuditInput,
) (int64, error) {
	now := time.Now().UTC()
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
			return err
		}
		var memoryBinding string
		var actorIdentityID uuid.UUID
		var rateLimit int
		var lastUsedAt, expiresAt, revokedAt sql.NullTime
		var createdAt time.Time
		var memorySpaceID sql.NullString
		if err := tx.WithContext(ctx).Raw(`
			SELECT COALESCE(credential.memory_binding, 'shared_only'), credential.actor_identity_id,
			       credential.rate_limit, credential.last_used_at, credential.expires_at,
			       credential.created_at, credential.revoked_at,
			       COALESCE(credential.memory_space_id::text, '')
			FROM credentials AS credential
			WHERE credential.id = $1 AND credential.team_id = $2 AND credential.kind = 'api_key'
			  AND credential.status <> 'disabled'
			FOR UPDATE
		`, id, teamID).Row().Scan(
			&memoryBinding, &actorIdentityID, &rateLimit, &lastUsedAt, &expiresAt,
			&createdAt, &revokedAt, &memorySpaceID,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}

		res := tx.WithContext(ctx).Exec(`
			UPDATE credentials
			SET status = 'disabled', revoked_at = COALESCE(revoked_at, $1), updated_at = $1
			WHERE id = $2 AND team_id = $3 AND kind = 'api_key' AND status <> 'disabled'
		`, now, id, teamID)
		if res.Error != nil {
			return res.Error
		}
		rowsAffected = res.RowsAffected
		if rowsAffected == 0 {
			return nil
		}
		if err := tx.WithContext(ctx).Exec(`
			UPDATE team_memberships
			SET status = 'revoked', updated_at = $1
			WHERE team_id = $2
			  AND actor_identity_id = $3
		`, now, teamID, actorIdentityID).Error; err != nil {
			return err
		}
		if auditInput != nil {
			if err := appendCredentialDeletionAuditTx(ctx, tx, teamID, id, memorySpaceID, now, rateLimit, lastUsedAt, expiresAt, createdAt, revokedAt, *auditInput); err != nil {
				return err
			}
		}

		if memoryBinding == string(domain.CredentialBindingCredentialPrivate) {
			return queueCredentialPrivateErasureTx(ctx, tx, teamID, id)
		}
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to delete api credential for team: %w", err)
	}
	if rowsAffected > 0 {
		if err := r.deactivateActorIfUnused(ctx, now, id); err != nil {
			return 0, fmt.Errorf("failed to reconcile deleted api credential actor: %w", err)
		}
	}

	return rowsAffected, nil
}

func appendCredentialDeletionAuditTx(
	ctx context.Context,
	tx *gorm.DB,
	teamID, credentialID uuid.UUID,
	memorySpaceID sql.NullString,
	now time.Time,
	rateLimit int,
	lastUsedAt, expiresAt sql.NullTime,
	createdAt time.Time,
	revokedAt sql.NullTime,
	input CredentialDeletionAuditInput,
) error {
	beforePayload, err := json.Marshal(map[string]any{
		"id":           credentialID.String(),
		"team_id":      teamID.String(),
		"rate_limit":   rateLimit,
		"last_used_at": timePtr(lastUsedAt),
		"expires_at":   timePtr(expiresAt),
		"created_at":   createdAt,
		"revoked_at":   timePtr(revokedAt),
	})
	if err != nil {
		return fmt.Errorf("marshal credential deletion audit: %w", err)
	}
	var actorCredentialID any
	if value := strings.TrimSpace(stringValue(input.ActorCredentialID)); value != "" {
		actorCredentialID = value
	}
	var clientIP any
	if value := strings.TrimSpace(input.ClientIP); value != "" {
		clientIP = value
	}
	var auditMemorySpaceID any
	if memorySpaceID.Valid && strings.TrimSpace(memorySpaceID.String) != "" {
		auditMemorySpaceID = memorySpaceID.String
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO audit_log (
			id, team_id, timestamp, operation, entity_type, entity_id,
			before_payload, after_payload, actor_profile_id, actor_role,
			client_ip, correlation_id, metadata, memory_space_id
		) VALUES (?, ?, ?, 'DELETE', 'api_key', ?, ?, NULL, ?, ?, ?, ?, '{}'::jsonb, ?)
	`, uuid.New(), teamID, now, credentialID.String(), beforePayload,
		actorCredentialID, input.ActorRole, clientIP, input.CorrelationID, auditMemorySpaceID).Error; err != nil {
		return fmt.Errorf("append credential deletion audit: %w", err)
	}
	return nil
}
