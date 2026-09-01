package repository

import (
	"context"
	"errors"
	"sync"

	"gorm.io/gorm"
)

const sharedAdvisoryLockAdmissionLimit = 4

var errAdvisoryLockAdmissionBusy = errors.New("advisory lock admission is busy")

type advisoryLockAdmissionState struct {
	slots chan struct{}
}

var advisoryLockAdmissions sync.Map // map[*sql.DB]*advisoryLockAdmissionState

func acquireSharedAdvisoryLockAdmission(ctx context.Context, db *gorm.DB, preferredLimit int) (func(), error) {
	if db == nil {
		return nil, errors.New("advisory lock admission: database is required")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	stateValue, loaded := advisoryLockAdmissions.Load(sqlDB)
	if !loaded {
		limit := preferredLimit
		if limit <= 0 || limit > sharedAdvisoryLockAdmissionLimit {
			limit = sharedAdvisoryLockAdmissionLimit
		}
		if configured := sqlDB.Stats().MaxOpenConnections; configured > 0 && configured-1 < limit {
			limit = configured - 1
		}
		if limit < 1 {
			return nil, errAdvisoryLockAdmissionBusy
		}
		stateValue, _ = advisoryLockAdmissions.LoadOrStore(sqlDB, &advisoryLockAdmissionState{
			slots: make(chan struct{}, limit),
		})
	}
	state := stateValue.(*advisoryLockAdmissionState)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	select {
	case state.slots <- struct{}{}:
		return func() { <-state.slots }, nil
	default:
		return nil, errAdvisoryLockAdmissionBusy
	}
}
