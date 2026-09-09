package repository

// This compatibility facade keeps existing repository callers source-compatible.
// PostgreSQL security persistence is implemented by internal/settings/postgres.

import (
	"gorm.io/gorm"

	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type SecurityRepository = settingscontract.SecurityRepository
type SecurityRepositoryImpl = settingspostgres.SecurityRepositoryImpl

func NewSecurityRepository(db *gorm.DB, rls postgres.RLSHelper) *SecurityRepositoryImpl {
	return settingspostgres.NewSecurityRepository(db, rls)
}
