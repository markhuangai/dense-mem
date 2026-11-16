//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRunnerSeedLogicalRestoreMigrationAndCredentialIdentity(t *testing.T) {
	f := newRunnerServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	require.NoError(t, f.native.Control(ctx, http.MethodPatch, "/config/ontology-maintenance", map[string]any{"items": []map[string]string{{"key": domain.AppConfigOntologyEnabled, "value": "false"}}}, nil))
	key := "dm_seed_fixed_credential_identity_canary_782"
	hash, err := crypto.HashKey(key)
	require.NoError(t, err)
	repo := accesspostgres.NewCredentialRepository(f.admin, storagepostgres.NewRLS(), nil)
	credential, err := repo.RotateForTeam(ctx, uuid.MustParse(f.native.Scope.TeamID), uuid.MustParse(f.native.Scope.CredentialID), hash, crypto.GetKeyPrefix(key), crypto.GetKeySuffix(key), nil)
	require.NoError(t, err)
	manifest := f.manifest
	manifest.ImportWorkers, manifest.Dataset, manifest.RegistryHash, manifest.BaseRelease, manifest.SourceRevision = 1, "synthetic", strings.Repeat("a", 64), "v2.8.1-rc.5", strings.Repeat("b", 40)
	manifest.SeedDir, manifest.ModelDeadline = t.TempDir(), time.Now().Add(time.Minute)
	manifest.RunID = "seed-roundtrip"
	f.native.Client.Capture = runnerSeedCapture(t)
	corpus := []evalharness.CorpusItem{{SourceDocID: "seed-document", Content: "Project Alpha stores durable memory in PostgreSQL."}}
	seed, err := evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	migrator := storagepostgres.NewMigratorWithDB(f.native.DB)
	var newest int64
	require.NoError(t, f.native.DB.QueryRowContext(ctx, "SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1").Scan(&newest))
	require.EqualValues(t, 20261114010000, newest, "the fixture rolls back only the verified no-op constraint-validation migration")
	require.NoError(t, migrator.RunDown(ctx))
	var applied bool
	require.NoError(t, f.native.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=$1 AND is_applied)", newest).Scan(&applied))
	require.False(t, applied)
	seed.SchemaVersion = 20261113020000
	_, err = f.native.DB.ExecContext(ctx, "INSERT INTO operation_logs(severity,severity_rank,message) VALUES ('DEBUG',10,$1)", "seed-provider-credential-canary-782")
	require.NoError(t, err)
	config, err := pgx.ParseConfig(f.admin.Dialector.(*gormpostgres.Dialector).Config.DSN)
	require.NoError(t, err)
	dir := t.TempDir()
	writeEnv := func(database string) string {
		file := filepath.Join(dir, database+".env")
		body := fmt.Sprintf("PGHOST=%s\nPGPORT=%d\nPGDATABASE=%s\nPGUSER=%s\nPGPASSWORD=%s\n", config.Host, config.Port, database, config.User, config.Password)
		require.NoError(t, os.WriteFile(file, []byte(body), 0600))
		return file
	}
	network := os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK")
	if network == "" {
		network = "host"
	}
	dumpPath := filepath.Join(manifest.SeedDir, "database.dump")
	file, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", network, "--env-file", writeEnv(config.Database), evalharness.PostgresImage, "pg_dump", "--format=custom", "--no-owner", "--no-privileges", "--exclude-table-data=public.operation_logs", "--exclude-table-data=public.remember_attempt_diagnostics", "--exclude-table-data=public.remember_invocation_diagnostics", "--exclude-table-data=public.remember_failure_artifacts")
	command.Stdout = file
	require.NoError(t, command.Run())
	require.NoError(t, file.Close())
	seed.DumpHash, err = evalharness.FileHash(dumpPath)
	require.NoError(t, err)
	info, err := os.Stat(dumpPath)
	require.NoError(t, err)
	seed.DumpBytes = info.Size()
	body, err := json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifest.SeedDir, "seed.json"), body, 0600))
	target := "seed_restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, f.native.DB.PingContext(ctx))
	_, err = f.native.DB.ExecContext(ctx, `CREATE DATABASE "`+target+`" TEMPLATE template0`)
	require.NoError(t, err)
	clientName := "seed-restore-" + uuid.NewString()
	create := exec.CommandContext(ctx, "docker", "create", "--name", clientName, "--network", network, "--env-file", writeEnv(target), evalharness.PostgresImage, "pg_restore", "--dbname", target, "--no-owner", "--no-privileges", "--clean", "--if-exists", "--exit-on-error", "--jobs=2", "/tmp/seed.dump")
	require.NoError(t, create.Run())
	t.Cleanup(func() {
		require.NoError(t, exec.Command("docker", "container", "rm", clientName).Run())
		_, dropErr := f.native.DB.Exec(`DROP DATABASE "` + target + `" WITH (FORCE)`)
		require.NoError(t, dropErr)
	})
	require.NoError(t, exec.CommandContext(ctx, "docker", "cp", dumpPath, clientName+":/tmp/seed.dump").Run())
	require.NoError(t, exec.CommandContext(ctx, "docker", "start", "--attach", clientName).Run())
	config.Database = target
	restored, err := gorm.Open(gormpostgres.Open(config.ConnString()), &gorm.Config{})
	require.NoError(t, err)
	restoredDB, err := restored.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restoredDB.Close()) })
	require.NoError(t, storagepostgres.NewMigratorWithDB(restoredDB).RunUp(ctx))
	for _, table := range []string{"operation_logs", "remember_attempt_diagnostics", "remember_invocation_diagnostics", "remember_failure_artifacts"} {
		var count int
		require.NoError(t, restoredDB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	require.NoError(t, restoredDB.QueryRowContext(ctx, "SELECT is_applied FROM goose_db_version WHERE version_id=$1 ORDER BY id DESC LIMIT 1", newest).Scan(&applied))
	require.True(t, applied)
	loaded, err := accesspostgres.NewCredentialRepository(restored, storagepostgres.NewRLS(), nil).GetActiveByPrefix(ctx, crypto.GetKeyPrefix(key))
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, credential.ID, loaded.ID)
	require.Equal(t, credential.MembershipID, loaded.MembershipID)
	require.Equal(t, credential.MemorySpaceID, loaded.MemorySpaceID)
	require.True(t, crypto.VerifyKey(key, loaded.KeyHash))
	require.False(t, crypto.VerifyKey("wrong-seed-key", loaded.KeyHash))
	native := *f.native
	native.DB = restoredDB
	beforeCalls := f.provider.calls
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.NoError(t, err)
	require.Equal(t, beforeCalls, f.provider.calls, "restoring and verifying a seed performs no Remember provider work")
	require.Equal(t, seed.Scope, native.Scope)
	seed.Dataset = "wrong-dataset"
	body, err = json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifest.SeedDir, "seed.json"), body, 0600))
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.ErrorContains(t, err, "dataset")
	seed.Dataset = manifest.Dataset
	seed.Scope.OwnerID = uuid.NewString()
	body, err = json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(manifest.SeedDir, "seed.json"), body, 0600))
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.ErrorContains(t, err, "scope differs")
}

func runnerSeedCapture(t *testing.T) *evalharness.Capture {
	t.Helper()
	return &evalharness.Capture{RunID: "seed-test", Logger: slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})), Protector: observability.NewCredentialProtector("dm_seed_fixed_credential_identity_canary_782")}
}

func runnerSealSyntheticSeed(t *testing.T, directory string, seed evalharness.SeedMetadata) evalharness.SeedMetadata {
	t.Helper()
	dump := []byte("synthetic checksum fixture; PostgreSQL serialization is covered by the logical restore regression")
	file := filepath.Join(directory, "database.dump")
	require.NoError(t, os.WriteFile(file, dump, 0600))
	var err error
	seed.DumpHash, err = evalharness.FileHash(file)
	require.NoError(t, err)
	seed.DumpBytes = int64(len(dump))
	body, err := json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "seed.json"), body, 0600))
	return seed
}
