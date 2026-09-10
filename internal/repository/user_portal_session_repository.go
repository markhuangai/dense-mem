package repository

// This compatibility facade keeps the historical repository import path
// source-compatible while Access owns the PostgreSQL implementation.

import (
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type UserPortalSessionRepository = accesspostgres.UserPortalSessionRepository
type UserPortalSessionRepositoryImpl = accesspostgres.UserPortalSessionRepositoryImpl

func NewUserPortalSessionRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *UserPortalSessionRepositoryImpl {
	return accesspostgres.NewUserPortalSessionRepository(db, rls)
}
