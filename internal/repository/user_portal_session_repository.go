package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// UserPortalSessionRepository stores opaque browser sessions in PostgreSQL.
// The repository is intentionally system-scoped because the session token is
// the credential used before a team/profile context exists.
type UserPortalSessionRepository interface {
	CreateSession(ctx context.Context, session *domain.UserPortalSession) error
	GetSession(ctx context.Context, sessionHash string) (*domain.UserPortalSession, error)
	DeleteSession(ctx context.Context, sessionHash string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error
}

type UserPortalSessionRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ UserPortalSessionRepository = (*UserPortalSessionRepositoryImpl)(nil)

func NewUserPortalSessionRepository(db *gorm.DB, rls postgres.RLSHelper) *UserPortalSessionRepositoryImpl {
	return &UserPortalSessionRepositoryImpl{db: db, rls: rls}
}

func (r *UserPortalSessionRepositoryImpl) CreateSession(ctx context.Context, session *domain.UserPortalSession) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO public.user_portal_sessions (
				session_hash, key_id, csrf_hash, expires_at, created_at
			) VALUES ($1, $2, $3, $4, $5)
		`, session.SessionHash, session.KeyID, session.CSRFHash, session.ExpiresAt, session.CreatedAt).Error
	})
	if err != nil {
		return fmt.Errorf("failed to create user portal session: %w", err)
	}
	return nil
}

func (r *UserPortalSessionRepositoryImpl) GetSession(ctx context.Context, sessionHash string) (*domain.UserPortalSession, error) {
	var session *domain.UserPortalSession
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT session_hash, key_id, csrf_hash, expires_at, created_at
			FROM public.user_portal_sessions
			WHERE session_hash = $1
			  AND expires_at > NOW()
		`, sessionHash).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}

		var scanned domain.UserPortalSession
		if err := rows.Scan(&scanned.SessionHash, &scanned.KeyID, &scanned.CSRFHash, &scanned.ExpiresAt, &scanned.CreatedAt); err != nil {
			return err
		}
		session = &scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get user portal session: %w", err)
	}
	return session, nil
}

func (r *UserPortalSessionRepositoryImpl) DeleteSession(ctx context.Context, sessionHash string) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM public.user_portal_sessions
			WHERE session_hash = $1
		`, sessionHash).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete user portal session: %w", err)
	}
	return nil
}

func (r *UserPortalSessionRepositoryImpl) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM public.user_portal_sessions
			WHERE expires_at <= $1
		`, now).Error
	})
	if err != nil {
		return fmt.Errorf("failed to delete expired user portal sessions: %w", err)
	}
	return nil
}
