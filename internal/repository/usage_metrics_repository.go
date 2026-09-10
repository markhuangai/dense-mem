package repository

// This compatibility facade preserves the historical usage-metrics
// constructor. Persistence is implemented by internal/operations/postgres.

import (
	"gorm.io/gorm"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	operationspostgres "github.com/markhuangai/dense-mem/internal/operations/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type UsageMetricsRepository = operationscontract.UsageMetricsRepository
type UsageMetricsRepositoryImpl = operationspostgres.UsageMetricsRepositoryImpl

func NewUsageMetricsRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *UsageMetricsRepositoryImpl {
	return operationspostgres.NewUsageMetricsRepository(db, rls)
}
