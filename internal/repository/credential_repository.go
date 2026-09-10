package repository

// This compatibility facade keeps the historical repository import path
// source-compatible while Access owns the PostgreSQL implementation.

import (
	"gorm.io/gorm"

	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type CredentialRepository = accesspostgres.CredentialRepository
type CredentialRepositoryImpl = accesspostgres.CredentialRepositoryImpl
type LastUsedUpdate = accesscontract.LastUsedUpdate

func NewCredentialRepository(db *gorm.DB, rls storagepostgres.RLSHelper) *CredentialRepositoryImpl {
	return accesspostgres.NewCredentialRepository(db, rls, privacypostgres.NewCredentialDeletionRepository(db, rls))
}

func GetKeyPrefixFromHash(hash string) string {
	return accesspostgres.GetKeyPrefixFromHash(hash)
}
