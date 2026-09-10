package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// CredentialDeletionRepository owns credential disablement, membership
// revocation, credential-linked audit, and private-memory retirement queueing.
// The caller remains responsible for any post-commit actor reconciliation.
type CredentialDeletionRepository struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

var _ privacycontract.CredentialDeletionStore = (*CredentialDeletionRepository)(nil)

func NewCredentialDeletionRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *CredentialDeletionRepository {
	return &CredentialDeletionRepository{db: db, rls: rls}
}

func (r *CredentialDeletionRepository) RetireCredential(ctx context.Context, teamID, id uuid.UUID, operationAt time.Time, auditInput *privacycontract.CredentialDeletionAuditInput) (int64, error) {
	if r == nil || r.db == nil || r.rls == nil {
		return 0, errors.New("credential deletion repository is unavailable")
	}
	now := operationAt
	var rowsAffected int64
	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		if err := storagepostgres.EnsureActiveTeamForMutation(ctx, tx, teamID.String()); err != nil {
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
			if err := AppendCredentialDeletionAuditTx(ctx, tx, teamID, id, memorySpaceID, now, rateLimit, lastUsedAt, expiresAt, createdAt, revokedAt, *auditInput); err != nil {
				return err
			}
		}
		if memoryBinding == string(domain.CredentialBindingCredentialPrivate) {
			return QueueCredentialPrivateErasureTx(ctx, tx, teamID, id)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to delete api credential for team: %w", err)
	}
	return rowsAffected, nil
}
