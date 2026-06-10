package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type AppConfigRepository interface {
	GetUpdateTime(ctx context.Context) (string, error)
	List(ctx context.Context) (map[string]domain.AppConfigEntry, error)
	UpdateValues(ctx context.Context, values map[string]string, updateTime string, now time.Time) (bool, error)
}

type AppConfigRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ AppConfigRepository = (*AppConfigRepositoryImpl)(nil)

func NewAppConfigRepository(db *gorm.DB, rls postgres.RLSHelper) *AppConfigRepositoryImpl {
	return &AppConfigRepositoryImpl{db: db, rls: rls}
}

func (r *AppConfigRepositoryImpl) GetUpdateTime(ctx context.Context) (string, error) {
	var updateTime string
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT value
			FROM app_config
			WHERE key = $1
		`, domain.AppConfigUpdateTimeKey).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return sql.ErrNoRows
		}
		return rows.Scan(&updateTime)
	})
	if err != nil {
		return "", fmt.Errorf("failed to get app config update time: %w", err)
	}
	return updateTime, nil
}

func (r *AppConfigRepositoryImpl) List(ctx context.Context) (map[string]domain.AppConfigEntry, error) {
	entries := make(map[string]domain.AppConfigEntry)
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT key, value, updated_at
			FROM app_config
			ORDER BY key ASC
		`).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry domain.AppConfigEntry
			if err := rows.Scan(&entry.Key, &entry.Value, &entry.UpdatedAt); err != nil {
				return err
			}
			entries[entry.Key] = entry
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list app config: %w", err)
	}
	return entries, nil
}

func (r *AppConfigRepositoryImpl) UpdateValues(ctx context.Context, values map[string]string, updateTime string, now time.Time) (bool, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	changed := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		for _, key := range keys {
			res := tx.Exec(`
				INSERT INTO app_config (key, value, updated_at)
				VALUES ($1, $2, $3)
				ON CONFLICT (key) DO UPDATE SET
					value = EXCLUDED.value,
					updated_at = EXCLUDED.updated_at
				WHERE app_config.value IS DISTINCT FROM EXCLUDED.value
			`, key, values[key], now)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return tx.Exec(`
			INSERT INTO app_config (key, value, updated_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (key) DO UPDATE SET
				value = EXCLUDED.value,
				updated_at = EXCLUDED.updated_at
		`, domain.AppConfigUpdateTimeKey, updateTime, now).Error
	})
	if err != nil {
		return false, fmt.Errorf("failed to update app config: %w", err)
	}
	return changed, nil
}

func (r *AppConfigRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithSystemTx(ctx, r.db, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}
