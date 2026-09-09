package repository

// This compatibility facade keeps existing repository callers source-compatible.
// PostgreSQL settings persistence is implemented by internal/settings/postgres.

import (
	"gorm.io/gorm"

	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type AppConfigRepository = settingscontract.AppConfigRepository
type AppConfigRepositoryImpl = settingspostgres.AppConfigRepositoryImpl

func NewAppConfigRepository(db *gorm.DB, rls postgres.RLSHelper) *AppConfigRepositoryImpl {
	return settingspostgres.NewAppConfigRepository(db, rls)
}
