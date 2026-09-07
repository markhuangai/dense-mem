package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres/lockadmission"
)

const sharedAdvisoryLockAdmissionLimit = 4

var errAdvisoryLockAdmissionBusy = errors.New("advisory lock admission is busy")

func acquireSharedAdvisoryLockAdmission(ctx context.Context, db *gorm.DB, preferredLimit int) (func(), error) {
	release, err := lockadmission.Acquire(ctx, db, preferredLimit)
	if errors.Is(err, lockadmission.ErrBusy) {
		return nil, errAdvisoryLockAdmissionBusy
	}
	return release, err
}
