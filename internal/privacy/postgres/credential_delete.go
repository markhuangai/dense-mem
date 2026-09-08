package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
)

type CredentialDeletionAuditInput = privacycontract.CredentialDeletionAuditInput

// QueueCredentialPrivateErasureTx is the privacy-owned port used by the
// existing credential transaction. It never opens or commits a transaction.
func QueueCredentialPrivateErasureTx(ctx context.Context, tx *gorm.DB, teamID, credentialID uuid.UUID) error {
	return withSystemModeInTx(ctx, tx, teamID.String(), teamID.String(), func(systemTx *gorm.DB) error {
		space, err := privateMemorySpaceForCredentialTx(ctx, systemTx, teamID, credentialID, false)
		if err != nil {
			return err
		}
		_, err = queuePrivateMemorySpaceTx(ctx, systemTx, space, queuePrivateMemoryInput{
			Action:               domain.PrivateMemoryRetireCredential,
			ActorClass:           domain.PrivateMemoryActorControl,
			ReasonCode:           "credential_deleted",
			TargetCredentialID:   &credentialID,
			RetireSpace:          true,
			QueueWhileHeld:       true,
			IdempotencyScopeHash: privateMemoryHash("team-credential-delete", teamID.String(), credentialID.String()),
			RequestHash:          privateMemoryHash("retire-credential", teamID.String(), credentialID.String()),
		})
		return err
	})
}

// AppendCredentialDeletionAuditTx writes the privacy-owned, scrubbed audit
// record inside the caller's credential transaction.
func AppendCredentialDeletionAuditTx(ctx context.Context, tx *gorm.DB, teamID, credentialID uuid.UUID, memorySpaceID sql.NullString, now time.Time, rateLimit int, lastUsedAt, expiresAt sql.NullTime, createdAt time.Time, revokedAt sql.NullTime, input CredentialDeletionAuditInput) error {
	beforePayload, err := json.Marshal(map[string]any{
		"id":           credentialID.String(),
		"team_id":      teamID.String(),
		"rate_limit":   rateLimit,
		"last_used_at": nullTimePtr(lastUsedAt),
		"expires_at":   nullTimePtr(expiresAt),
		"created_at":   createdAt,
		"revoked_at":   nullTimePtr(revokedAt),
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

func withSystemModeInTx(ctx context.Context, tx *gorm.DB, teamID, profileID string, fn func(*gorm.DB) error) error {
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', '', true)").Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_profile_id', '', true)").Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.tx_mode', 'system', true)").Error; err != nil {
		return err
	}
	fnErr := fn(tx)
	resetErr := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error
	if resetErr == nil {
		resetErr = tx.WithContext(ctx).Exec("SELECT set_config('app.current_profile_id', ?, true)", profileID).Error
	}
	if resetErr == nil {
		resetErr = tx.WithContext(ctx).Exec("SELECT set_config('app.tx_mode', 'profile', true)").Error
	}
	if fnErr != nil {
		return fnErr
	}
	return resetErr
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}
