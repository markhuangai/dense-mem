package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/postgrescompat"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type ControlIdentityRepository interface {
	ListControlAdminGroups(ctx context.Context, providerID uuid.UUID) ([]*domain.ControlAdminGroup, error)
	CreateControlAdminGroup(ctx context.Context, group *domain.ControlAdminGroup) error
	UpdateControlAdminGroup(ctx context.Context, group *domain.ControlAdminGroup) error
	RetireControlAdminGroup(ctx context.Context, providerID, groupID uuid.UUID) error

	CreateControlOAuthState(ctx context.Context, state domain.ControlOAuthState) error
	ConsumeControlOAuthState(ctx context.Context, stateHash string) (*domain.ControlOAuthState, error)
	DeleteExpiredControlOAuthStates(ctx context.Context, now time.Time) error

	CreateControlSession(ctx context.Context, session domain.ControlSession) error
	GetControlSession(ctx context.Context, sessionHash string) (*domain.ControlSession, error)
	DeleteControlSession(ctx context.Context, sessionHash string) error
	DeleteExpiredControlSessions(ctx context.Context, now time.Time) error
}

type ControlIdentityRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ ControlIdentityRepository = (*ControlIdentityRepositoryImpl)(nil)

func NewControlIdentityRepository(db *gorm.DB, rls postgres.RLSHelper) *ControlIdentityRepositoryImpl {
	return &ControlIdentityRepositoryImpl{db: db, rls: rls}
}

func (r *ControlIdentityRepositoryImpl) ListControlAdminGroups(ctx context.Context, providerID uuid.UUID) ([]*domain.ControlAdminGroup, error) {
	groups := make([]*domain.ControlAdminGroup, 0)
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, provider_id, group_id, group_name, enabled, retired_at, created_at, updated_at
			FROM sso_control_admin_groups
			WHERE provider_id = $1
			ORDER BY group_name ASC, group_id ASC
		`, providerID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			group, err := scanControlAdminGroup(rows)
			if err != nil {
				return err
			}
			groups = append(groups, group)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list control admin groups: %w", err)
	}
	return groups, nil
}

func (r *ControlIdentityRepositoryImpl) CreateControlAdminGroup(ctx context.Context, group *domain.ControlAdminGroup) error {
	if group == nil || group.ProviderID == uuid.Nil || strings.TrimSpace(group.GroupID) == "" {
		return fmt.Errorf("control admin provider ID and group ID are required")
	}
	if group.ID == uuid.Nil {
		group.ID = uuid.New()
	}
	now := time.Now().UTC()
	group.CreatedAt, group.UpdatedAt = now, now
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_control_admin_groups (id, provider_id, group_id, group_name, enabled, retired_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, NULL, $6, $6)
		`, group.ID, group.ProviderID, group.GroupID, group.GroupName, group.Enabled, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create control admin group: %w", err)
	}
	return nil
}

func (r *ControlIdentityRepositoryImpl) UpdateControlAdminGroup(ctx context.Context, group *domain.ControlAdminGroup) error {
	if group == nil || group.ID == uuid.Nil || group.ProviderID == uuid.Nil || strings.TrimSpace(group.GroupID) == "" {
		return fmt.Errorf("control admin group ID, provider ID, and group ID are required")
	}
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_control_admin_groups
			SET group_id = $1,
			    group_name = $2,
			    enabled = $3,
			    retired_at = CASE WHEN $3 THEN NULL ELSE COALESCE(retired_at, $4) END,
			    updated_at = $4
			WHERE id = $5 AND provider_id = $6
		`, group.GroupID, group.GroupName, group.Enabled, now, group.ID, group.ProviderID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to update control admin group: %w", err)
	}
	group.UpdatedAt = now
	return nil
}

func (r *ControlIdentityRepositoryImpl) RetireControlAdminGroup(ctx context.Context, providerID, groupID uuid.UUID) error {
	now := time.Now().UTC()
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		res := tx.Exec(`
			UPDATE sso_control_admin_groups
			SET enabled = false, retired_at = COALESCE(retired_at, $1), updated_at = $1
			WHERE id = $2 AND provider_id = $3
		`, now, groupID, providerID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to retire control admin group: %w", err)
	}
	return nil
}

func (r *ControlIdentityRepositoryImpl) CreateControlOAuthState(ctx context.Context, state domain.ControlOAuthState) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_control_oauth_states (state_hash, provider_id, pkce_verifier, nonce, expires_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, state.StateHash, state.ProviderID, state.PKCEVerifier, state.Nonce, state.ExpiresAt, state.CreatedAt).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create control oauth state: %w", err)
	}
	return nil
}

func (r *ControlIdentityRepositoryImpl) ConsumeControlOAuthState(ctx context.Context, stateHash string) (*domain.ControlOAuthState, error) {
	var state *domain.ControlOAuthState
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			DELETE FROM sso_control_oauth_states
			WHERE state_hash = $1 AND expires_at > NOW()
			RETURNING state_hash, provider_id, pkce_verifier, nonce, expires_at, created_at
		`, stateHash).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanControlOAuthState(rows)
		if err != nil {
			return err
		}
		state = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to consume control oauth state: %w", err)
	}
	return state, nil
}

func (r *ControlIdentityRepositoryImpl) DeleteExpiredControlOAuthStates(ctx context.Context, now time.Time) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_control_oauth_states WHERE expires_at <= $1`, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete expired control oauth states: %w", err)
	}
	return nil
}

func (r *ControlIdentityRepositoryImpl) CreateControlSession(ctx context.Context, session domain.ControlSession) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO sso_control_sessions (session_hash, identity_id, provider_id, group_ids, csrf_hash, expires_at, created_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, session.SessionHash, session.IdentityID, session.ProviderID, postgrescompat.Array(session.GroupIDs), session.CSRFHash, session.ExpiresAt, session.CreatedAt, session.LastSeenAt).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create control session: %w", err)
	}
	return nil
}

func (r *ControlIdentityRepositoryImpl) GetControlSession(ctx context.Context, sessionHash string) (*domain.ControlSession, error) {
	var session *domain.ControlSession
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT session_hash, identity_id, provider_id, group_ids, csrf_hash, expires_at, created_at, last_seen_at
			FROM sso_control_sessions
			WHERE session_hash = $1
		`, sessionHash).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanControlSession(rows)
		if err != nil {
			return err
		}
		session = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get control session: %w", err)
	}
	return session, nil
}

func (r *ControlIdentityRepositoryImpl) DeleteControlSession(ctx context.Context, sessionHash string) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_control_sessions WHERE session_hash = $1`, sessionHash).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete control session: %w", err)
	}
	return nil
}

func (r *ControlIdentityRepositoryImpl) DeleteExpiredControlSessions(ctx context.Context, now time.Time) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM sso_control_sessions WHERE expires_at <= $1`, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete expired control sessions: %w", err)
	}
	return nil
}

func scanControlAdminGroup(rows *sql.Rows) (*domain.ControlAdminGroup, error) {
	var group domain.ControlAdminGroup
	if err := rows.Scan(&group.ID, &group.ProviderID, &group.GroupID, &group.GroupName, &group.Enabled, &group.RetiredAt, &group.CreatedAt, &group.UpdatedAt); err != nil {
		return nil, err
	}
	return &group, nil
}

func scanControlOAuthState(rows *sql.Rows) (*domain.ControlOAuthState, error) {
	var state domain.ControlOAuthState
	if err := rows.Scan(&state.StateHash, &state.ProviderID, &state.PKCEVerifier, &state.Nonce, &state.ExpiresAt, &state.CreatedAt); err != nil {
		return nil, err
	}
	return &state, nil
}

func scanControlSession(rows *sql.Rows) (*domain.ControlSession, error) {
	var session domain.ControlSession
	if err := rows.Scan(&session.SessionHash, &session.IdentityID, &session.ProviderID, postgrescompat.Array(&session.GroupIDs), &session.CSRFHash, &session.ExpiresAt, &session.CreatedAt, &session.LastSeenAt); err != nil {
		return nil, err
	}
	return &session, nil
}
