package dreamservice

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type PostgresCycleLocker struct{}

func NewPostgresCycleLocker() *PostgresCycleLocker {
	return &PostgresCycleLocker{}
}

func (l *PostgresCycleLocker) WithCycleLock(ctx context.Context, db *gorm.DB, teamID, runDate string, timeout time.Duration, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return fmt.Errorf("dreaming cycle lock: postgres db is required")
	}
	key := "dreaming:" + teamID + ":" + runDate
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockCtx, cancel := context.WithTimeout(ctx, timeout)
		err := tx.WithContext(lockCtx).Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error
		cancel()
		if err != nil {
			return fmt.Errorf("dreaming cycle lock acquire (team=%s date=%s): %w", teamID, runDate, err)
		}
		return fn(tx)
	})
}
