package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func bootstrapCredential(ctx context.Context, manifest evalharness.Manifest) error {
	if manifest.Dataset != "bootstrap" || os.Getenv("EVAL_API_KEY") == "" {
		return errors.New("bootstrap requires its owned empty runtime and a configured team key")
	}
	team, err := uuid.Parse(os.Getenv("EVAL_TEAM_ID"))
	if err != nil || team == uuid.Nil {
		return errors.New("invalid bootstrap team")
	}
	database, err := sql.Open("pgx", os.Getenv("EVAL_DATABASE_URL"))
	if err != nil {
		return errors.New("bootstrap database configuration is invalid")
	}
	defer database.Close()
	scope, err := bootstrapIdentity(ctx, database, team, os.Getenv("EVAL_CREDENTIAL_ID"), os.Getenv("EVAL_API_KEY"))
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(manifest.SeedDir, "scope.json"), append(data, '\n'), 0600)
}

func bootstrapIdentity(ctx context.Context, database *sql.DB, team uuid.UUID, expectedCredentialID, key string) (evalharness.Scope, error) {
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: database}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return evalharness.Scope{}, err
	}
	var evidenceCount, credentialCount int64
	if err = database.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM evidence_fragments),(SELECT count(*) FROM credentials)").Scan(&evidenceCount, &credentialCount); err != nil {
		return evalharness.Scope{}, err
	}
	if evidenceCount != 0 || credentialCount > 1 {
		return evalharness.Scope{}, errors.New("bootstrap requires zero Evidence and at most its one credential")
	}
	repository := accesspostgres.NewCredentialRepository(db, storagepostgres.NewRLS(), nil)
	var credential *domain.Credential
	if credentialCount == 1 {
		credential, err = repository.GetActiveByPrefix(ctx, crypto.GetKeyPrefix(key))
		if err != nil {
			return evalharness.Scope{}, err
		}
		if credential == nil || credential.TeamID != team || credential.ID.String() != expectedCredentialID || !crypto.VerifyKey(key, credential.KeyHash) {
			return evalharness.Scope{}, errors.New("configured team key does not match the preserved bootstrap credential")
		}
	} else {
		hash, hashErr := crypto.HashKey(key)
		if hashErr != nil {
			return evalharness.Scope{}, hashErr
		}
		credential = &domain.Credential{ID: uuid.New(), TeamID: team, Name: "evaluation runner", Role: "manager", Scopes: []string{"read", "write"}, RateLimit: 200000, KeyHash: hash, KeyPrefix: crypto.GetKeyPrefix(key), KeySuffix: crypto.GetKeySuffix(key)}
		if err = repository.CreateCredential(ctx, credential); err != nil {
			return evalharness.Scope{}, err
		}
	}
	scope := evalharness.Scope{TeamID: team.String(), CredentialID: credential.ID.String()}
	native := &evalharness.Native{DB: database, Scope: scope}
	if err = native.ResolveScope(ctx); err != nil {
		return evalharness.Scope{}, err
	}
	return native.Scope, nil
}
