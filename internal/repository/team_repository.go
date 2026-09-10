package repository

// This compatibility facade keeps the historical repository import path
// source-compatible while Access owns the PostgreSQL implementation.

import (
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type TeamRepository = accesspostgres.TeamRepository
type TeamRepositoryImpl = accesspostgres.TeamRepositoryImpl

func NewTeamRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *TeamRepositoryImpl {
	return accesspostgres.NewTeamRepository(db, rls)
}
