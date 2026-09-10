package repository

// This compatibility facade keeps the historical repository import path
// source-compatible while Access owns the PostgreSQL implementation.

import (
	"gorm.io/gorm"

	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type SSORepository = accesspostgres.SSORepository
type SSORepositoryImpl = accesspostgres.SSORepositoryImpl

var (
	ErrSSOProtectedResourceProfileLimit = accesscontract.ErrSSOProtectedResourceProfileLimit
)

func NewSSORepository(db *gorm.DB, rls storagepostgres.RLSHelper) *SSORepositoryImpl {
	return accesspostgres.NewSSORepository(db, rls)
}
