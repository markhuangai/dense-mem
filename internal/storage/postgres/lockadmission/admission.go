// Package lockadmission bounds session-advisory-lock users against one
// database pool. It contains coordination mechanics only; callers retain
// ownership of lock keys and transaction policy.
package lockadmission

import (
	"context"
	"errors"
	"sync"

	"gorm.io/gorm"
)

const maxSharedAdmissionLimit = 4

var ErrBusy = errors.New("advisory lock admission is busy")

type state struct {
	slots chan struct{}
}

var admissions sync.Map // map[*sql.DB]*state

// Configure fixes a pool's capacity before first use; existing gates cannot be resized.
func Configure(db *gorm.DB, limit int) error {
	if db == nil {
		return errors.New("advisory lock admission: database is required")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	poolLimit := sqlDB.Stats().MaxOpenConnections
	if limit < 1 || poolLimit < 2 || limit >= poolLimit {
		return errors.New("advisory lock admission: capacity must be positive and leave a query connection in a bounded pool")
	}
	value, loaded := admissions.LoadOrStore(sqlDB, &state{slots: make(chan struct{}, limit)})
	if loaded && cap(value.(*state).slots) != limit {
		return errors.New("advisory lock admission: pool capacity is already initialized")
	}
	return nil
}

func Acquire(ctx context.Context, db *gorm.DB, preferredLimit int) (func(), error) {
	if db == nil {
		return nil, errors.New("advisory lock admission: database is required")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	value, loaded := admissions.Load(sqlDB)
	if !loaded {
		limit := preferredLimit
		if limit <= 0 || limit > maxSharedAdmissionLimit {
			limit = maxSharedAdmissionLimit
		}
		if configured := sqlDB.Stats().MaxOpenConnections; configured > 0 && configured-1 < limit {
			limit = configured - 1
		}
		if limit < 1 {
			return nil, ErrBusy
		}
		value, _ = admissions.LoadOrStore(sqlDB, &state{slots: make(chan struct{}, limit)})
	}
	admission := value.(*state)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	select {
	case admission.slots <- struct{}{}:
		return func() { <-admission.slots }, nil
	default:
		return nil, ErrBusy
	}
}
