package repository

// This compatibility facade keeps the historical repository import path
// source-compatible while Access owns the PostgreSQL implementation.

import (
	"gorm.io/gorm"

	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type DirectoryIdentityRepository = accesspostgres.DirectoryIdentityRepository
type DirectoryIdentityRepositoryImpl = accesspostgres.DirectoryIdentityRepositoryImpl

var (
	ErrDirectoryResourceConflict = accesscontract.ErrDirectoryResourceConflict
	ErrDirectoryInvalidValue     = accesscontract.ErrDirectoryInvalidValue
	ErrDirectoryReconcileStale   = accesscontract.ErrDirectoryReconcileStale
)

func NewDirectoryIdentityRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *DirectoryIdentityRepositoryImpl {
	return accesspostgres.NewDirectoryIdentityRepository(db, rls)
}
