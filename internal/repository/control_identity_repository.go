package repository

// This compatibility facade keeps the historical repository import path
// source-compatible while Access owns the PostgreSQL implementation.

import (
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type ControlIdentityRepository = accesspostgres.ControlIdentityRepository
type ControlIdentityRepositoryImpl = accesspostgres.ControlIdentityRepositoryImpl

func NewControlIdentityRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *ControlIdentityRepositoryImpl {
	return accesspostgres.NewControlIdentityRepository(db, rls)
}
