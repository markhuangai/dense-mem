//go:build integration

package postgres

import (
	sessionpostgres "github.com/markhuangai/dense-mem/internal/session/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"gorm.io/gorm"
)

type sessionRepository = sessionpostgres.Store

type sessionFixtureStore struct {
	*Store
	*sessionRepository
}

func newSessionFixtureStore(db *gorm.DB, rls storage.RLSHelper) *sessionFixtureStore {
	evidence := NewStore(db, rls)
	return &sessionFixtureStore{evidence, sessionpostgres.New(db, rls, evidence.CommitEvidenceTx, evidence.WithSubmissionLock)}
}
