package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type AppConfigRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ settingscontract.AppConfigRepository = (*AppConfigRepositoryImpl)(nil)

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
		if err := rows.Scan(&updateTime); err != nil {
			return err
		}
		return rows.Err()
	})
	if err != nil {
		return "", fmt.Errorf("failed to get app config update time: %w", err)
	}
	return updateTime, nil
}

func (r *AppConfigRepositoryImpl) List(ctx context.Context) (map[string]domain.AppConfigEntry, error) {
	var entries map[string]domain.AppConfigEntry
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var err error
		entries, err = listAppConfigEntries(tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list app config: %w", err)
	}
	return entries, nil
}

func listAppConfigEntries(tx *gorm.DB) (map[string]domain.AppConfigEntry, error) {
	rows, err := tx.Raw("SELECT key, value, updated_at FROM app_config ORDER BY key ASC").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make(map[string]domain.AppConfigEntry)
	for rows.Next() {
		var entry domain.AppConfigEntry
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		entries[entry.Key] = entry
	}
	return entries, rows.Err()
}

func (r *AppConfigRepositoryImpl) UpdateValues(ctx context.Context, values map[string]string, updateTime string, now time.Time, prepare ...settingscontract.PrepareConfigUpdate) (bool, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	changed := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		var before map[string]domain.AppConfigEntry
		if len(prepare) != 0 {
			var locked string
			if err := tx.Raw("SELECT value FROM app_config WHERE key = ? FOR UPDATE", domain.AppConfigUpdateTimeKey).Row().Scan(&locked); err != nil {
				return fmt.Errorf("lock settings revision: %w", err)
			}
			var err error
			before, err = listAppConfigEntries(tx)
			if err != nil {
				return err
			}
		}
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
		if changed {
			if err := tx.Exec(`
                INSERT INTO app_config (key, value, updated_at)
                VALUES ($1, $2, $3)
                ON CONFLICT (key) DO UPDATE SET
                    value = EXCLUDED.value,
                    updated_at = EXCLUDED.updated_at
            `, domain.AppConfigUpdateTimeKey, updateTime, now).Error; err != nil {
				return err
			}
		}
		if len(prepare) == 0 {
			return nil
		}
		after := before
		if changed {
			var err error
			after, err = listAppConfigEntries(tx)
			if err != nil {
				return err
			}
		}
		entry, err := prepare[0](before, after, changed)
		if err != nil || entry == nil {
			return err
		}
		return postgres.InsertAuditEntryTx(ctx, tx, *entry)

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
