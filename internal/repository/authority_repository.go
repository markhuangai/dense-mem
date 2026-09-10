package repository

// This compatibility facade preserves the historical authority constructor.
// Bootstrap persistence is implemented by internal/operations/postgres.

import (
	"gorm.io/gorm"

	operationscontract "github.com/markhuangai/dense-mem/internal/operations/contract"
	operationspostgres "github.com/markhuangai/dense-mem/internal/operations/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var ErrFreshAuthorityBlocked = operationspostgres.ErrFreshAuthorityBlocked

type CommitFreshAuthorityInput = operationscontract.CommitFreshAuthorityInput
type AuthorityRepository = operationspostgres.AuthorityRepository

func NewAuthorityRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *AuthorityRepository {
	return operationspostgres.NewAuthorityRepository(db, rls)
}
