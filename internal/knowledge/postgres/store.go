package postgres

import (
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

type RememberPort = knowledgecontract.RememberPort
type LifecyclePort = knowledgecontract.LifecyclePort

func NewStore(db *gorm.DB, rls storagepostgres.RLSHelper) *Store {
	return &Store{db: db, rls: rls, rememberIdempotencyLocks: make(map[string]*rememberIdempotencyLockEntry)}
}
func New(db *gorm.DB, rls storagepostgres.RLSHelper) *Store { return NewStore(db, rls) }

var _ knowledgecontract.RememberPort = (*Store)(nil)
var _ knowledgecontract.LifecyclePort = (*Store)(nil)
