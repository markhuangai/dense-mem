package repository

// This compatibility facade preserves the historical operation-log
// constructor. Persistence is implemented by internal/operations/postgres.

import (
	"gorm.io/gorm"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	operationspostgres "github.com/markhuangai/dense-mem/internal/operations/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type OperationLogRepository = operationscontract.OperationLogRepository
type OperationLogRepositoryImpl = operationspostgres.OperationLogRepositoryImpl

func NewOperationLogRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *OperationLogRepositoryImpl {
	return operationspostgres.NewOperationLogRepository(db, rls)
}
