package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// SecurityRepository persists app-level security settings, counters, and bans.
type SecurityRepository interface {
	GetSettings(ctx context.Context) (*domain.SecuritySettings, error)
	UpdateSettings(ctx context.Context, settings domain.SecuritySettings) (*domain.SecuritySettings, error)
	GetActiveBan(ctx context.Context, ip string, now time.Time) (*domain.SecurityIPBan, error)
	RecordFailure(ctx context.Context, ip, surface, reason string, windowSeconds int, now time.Time) (*domain.SecurityIPFailure, error)
	UpsertBan(ctx context.Context, ban *domain.SecurityIPBan) error
	ListBans(ctx context.Context, includeExpired bool, limit, offset int) ([]domain.SecurityIPBan, int64, error)
	DeleteBan(ctx context.Context, ip string, now time.Time) error
}

type SecurityRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ SecurityRepository = (*SecurityRepositoryImpl)(nil)

func NewSecurityRepository(db *gorm.DB, rls postgres.RLSHelper) *SecurityRepositoryImpl {
	return &SecurityRepositoryImpl{db: db, rls: rls}
}

func (r *SecurityRepositoryImpl) GetSettings(ctx context.Context) (*domain.SecuritySettings, error) {
	settings := domain.SecuritySettings{
		Enabled:              true,
		FailureThreshold:     10,
		FailureWindowSeconds: 600,
		BanDurationSeconds:   0,
	}
	found := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
			SELECT enabled, failure_threshold, failure_window_seconds, ban_duration_seconds, created_at, updated_at
			FROM security_settings
			WHERE id = true
		`).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()
		if rows.Next() {
			found = true
			return rows.Scan(
				&settings.Enabled,
				&settings.FailureThreshold,
				&settings.FailureWindowSeconds,
				&settings.BanDurationSeconds,
				&settings.CreatedAt,
				&settings.UpdatedAt,
			)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get security settings: %w", err)
	}
	if !found {
		now := time.Now().UTC()
		settings.CreatedAt = now
		settings.UpdatedAt = now
	}
	return &settings, nil
}

func (r *SecurityRepositoryImpl) UpdateSettings(ctx context.Context, settings domain.SecuritySettings) (*domain.SecuritySettings, error) {
	now := time.Now().UTC()
	var updated domain.SecuritySettings
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
			INSERT INTO security_settings (
				id, enabled, failure_threshold, failure_window_seconds,
				ban_duration_seconds, created_at, updated_at
			) VALUES (
				true, $1, $2, $3, $4, $5, $5
			)
			ON CONFLICT (id) DO UPDATE SET
				enabled = EXCLUDED.enabled,
				failure_threshold = EXCLUDED.failure_threshold,
				failure_window_seconds = EXCLUDED.failure_window_seconds,
				ban_duration_seconds = EXCLUDED.ban_duration_seconds,
				updated_at = EXCLUDED.updated_at
			RETURNING enabled, failure_threshold, failure_window_seconds, ban_duration_seconds, created_at, updated_at
		`, settings.Enabled, settings.FailureThreshold, settings.FailureWindowSeconds, settings.BanDurationSeconds, now).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()
		if !rows.Next() {
			return sql.ErrNoRows
		}
		return rows.Scan(
			&updated.Enabled,
			&updated.FailureThreshold,
			&updated.FailureWindowSeconds,
			&updated.BanDurationSeconds,
			&updated.CreatedAt,
			&updated.UpdatedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update security settings: %w", err)
	}
	return &updated, nil
}

func (r *SecurityRepositoryImpl) GetActiveBan(ctx context.Context, ip string, now time.Time) (*domain.SecurityIPBan, error) {
	var ban *domain.SecurityIPBan
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
			SELECT ip, reason, source, failure_count, banned_at, expires_at, last_failed_at, metadata, created_at, updated_at, revoked_at
			FROM security_ip_bans
			WHERE ip = $1
				AND revoked_at IS NULL
				AND (expires_at IS NULL OR expires_at > $2)
		`, ip, now).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()
		if rows.Next() {
			scanned, serr := scanSecurityBan(rows)
			if serr != nil {
				return serr
			}
			ban = &scanned
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get active security ban: %w", err)
	}
	return ban, nil
}

func (r *SecurityRepositoryImpl) RecordFailure(ctx context.Context, ip, surface, reason string, windowSeconds int, now time.Time) (*domain.SecurityIPFailure, error) {
	windowStart := now.Add(-time.Duration(windowSeconds) * time.Second)
	var failure domain.SecurityIPFailure
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, rerr := tx.Raw(`
			INSERT INTO security_ip_failures (
				ip, failure_count, first_failed_at, last_failed_at,
				last_reason, last_surface, updated_at
			) VALUES (
				$1, 1, $2, $2, $3, $4, $2
			)
			ON CONFLICT (ip) DO UPDATE SET
				failure_count = CASE
					WHEN security_ip_failures.first_failed_at < $5 THEN 1
					ELSE security_ip_failures.failure_count + 1
				END,
				first_failed_at = CASE
					WHEN security_ip_failures.first_failed_at < $5 THEN EXCLUDED.first_failed_at
					ELSE security_ip_failures.first_failed_at
				END,
				last_failed_at = EXCLUDED.last_failed_at,
				last_reason = EXCLUDED.last_reason,
				last_surface = EXCLUDED.last_surface,
				updated_at = EXCLUDED.updated_at
			RETURNING ip, failure_count, first_failed_at, last_failed_at, last_reason, last_surface, updated_at
		`, ip, now, reason, surface, windowStart).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()
		if !rows.Next() {
			return sql.ErrNoRows
		}
		return rows.Scan(
			&failure.IP,
			&failure.FailureCount,
			&failure.FirstFailedAt,
			&failure.LastFailedAt,
			&failure.LastReason,
			&failure.LastSurface,
			&failure.UpdatedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to record security failure: %w", err)
	}
	return &failure, nil
}

func (r *SecurityRepositoryImpl) UpsertBan(ctx context.Context, ban *domain.SecurityIPBan) error {
	if ban == nil {
		return fmt.Errorf("security ban is required")
	}
	if ban.Metadata == nil {
		ban.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(ban.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal security ban metadata: %w", err)
	}
	return r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO security_ip_bans (
				ip, reason, source, failure_count, banned_at, expires_at,
				last_failed_at, metadata, created_at, updated_at, revoked_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $9, NULL
			)
			ON CONFLICT (ip) DO UPDATE SET
				reason = EXCLUDED.reason,
				source = EXCLUDED.source,
				failure_count = EXCLUDED.failure_count,
				banned_at = EXCLUDED.banned_at,
				expires_at = EXCLUDED.expires_at,
				last_failed_at = EXCLUDED.last_failed_at,
				metadata = EXCLUDED.metadata,
				updated_at = EXCLUDED.updated_at,
				revoked_at = NULL
		`, ban.IP, ban.Reason, ban.Source, ban.FailureCount, ban.BannedAt, ban.ExpiresAt, ban.LastFailedAt, string(metadata), ban.CreatedAt).Error
	})
}

func (r *SecurityRepositoryImpl) ListBans(ctx context.Context, includeExpired bool, limit, offset int) ([]domain.SecurityIPBan, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	whereClause := securityBanListWhereClause(includeExpired)

	var bans []domain.SecurityIPBan
	var total int64
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT count(*) FROM security_ip_bans " + whereClause).Scan(&total).Error; err != nil {
			return err
		}
		rows, rerr := tx.Raw(`
			SELECT ip, reason, source, failure_count, banned_at, expires_at, last_failed_at, metadata, created_at, updated_at, revoked_at
			FROM security_ip_bans
			`+whereClause+`
			ORDER BY banned_at DESC, ip ASC
			LIMIT $1 OFFSET $2
		`, limit, offset).Rows()
		if rerr != nil {
			return rerr
		}
		defer rows.Close()
		for rows.Next() {
			ban, serr := scanSecurityBan(rows)
			if serr != nil {
				return serr
			}
			bans = append(bans, ban)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list security bans: %w", err)
	}
	return bans, total, nil
}

func securityBanListWhereClause(includeExpired bool) string {
	whereClause := "WHERE revoked_at IS NULL"
	if !includeExpired {
		whereClause += " AND (expires_at IS NULL OR expires_at > NOW())"
	}
	return whereClause
}

func (r *SecurityRepositoryImpl) DeleteBan(ctx context.Context, ip string, now time.Time) error {
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE security_ip_bans
			SET revoked_at = $2, updated_at = $2
			WHERE ip = $1 AND revoked_at IS NULL
		`, ip, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			DELETE FROM security_ip_failures
			WHERE ip = $1
		`, ip).Error
	})
	if err != nil {
		return fmt.Errorf("failed to revoke security ban: %w", err)
	}
	return nil
}

func (r *SecurityRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithSystemTx(ctx, r.db, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func scanSecurityBan(rows interface {
	Scan(dest ...any) error
}) (domain.SecurityIPBan, error) {
	var ban domain.SecurityIPBan
	var expiresAt, lastFailedAt, revokedAt sql.NullTime
	var metadataJSON []byte
	if err := rows.Scan(
		&ban.IP,
		&ban.Reason,
		&ban.Source,
		&ban.FailureCount,
		&ban.BannedAt,
		&expiresAt,
		&lastFailedAt,
		&metadataJSON,
		&ban.CreatedAt,
		&ban.UpdatedAt,
		&revokedAt,
	); err != nil {
		return ban, err
	}
	if expiresAt.Valid {
		ban.ExpiresAt = &expiresAt.Time
	}
	if lastFailedAt.Valid {
		ban.LastFailedAt = &lastFailedAt.Time
	}
	if revokedAt.Valid {
		ban.RevokedAt = &revokedAt.Time
	}
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &ban.Metadata); err != nil {
			return ban, err
		}
	}
	if ban.Metadata == nil {
		ban.Metadata = map[string]any{}
	}
	return ban, nil
}
