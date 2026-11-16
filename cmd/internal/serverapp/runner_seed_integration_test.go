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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	containerapi "github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
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
	capture := f.native.Client.Capture
	capture.Protector = evalharness.NewRunnerCredentialProtector(key, config.Password)
	options := []testcontainers.ContainerCustomizer{
		testcontainers.WithEntrypoint("sleep"), testcontainers.WithCmd("300"),
		testcontainers.WithEnv(map[string]string{"PGHOST": config.Host, "PGPORT": fmt.Sprint(config.Port), "PGDATABASE": config.Database, "PGUSER": config.User, "PGPASSWORD": config.Password}),
		testcontainers.WithWaitStrategy(wait.ForExec([]string{"true"}).WithStartupTimeout(time.Minute)),
	}
	if network := os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK"); network != "" {
		options = append(options, tcnetwork.WithNetworkName(nil, network))
	} else {
		options = append(options, testcontainers.WithHostConfigModifier(func(host *containerapi.HostConfig) { host.NetworkMode = "host" }))
	}
	client, err := testcontainers.Run(ctx, evalharness.PostgresImage, options...)
	require.Empty(t, capture.ProtectError(err))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.Empty(t, capture.ProtectError(client.Terminate(cleanup)))
	})
	runPostgres := func(arguments ...string) string {
		exitCode, output, err := client.Exec(ctx, arguments, tcexec.Multiplexed())
		require.Empty(t, capture.ProtectError(err))
		details, err := io.ReadAll(io.LimitReader(output, (1<<20)+1))
		require.Empty(t, capture.ProtectError(err))
		require.LessOrEqual(t, len(details), 1<<20, "PostgreSQL command diagnostics exceeded capture bounds")
		require.Zero(t, exitCode, capture.ProtectError(fmt.Errorf("%s: %s", arguments[0], details)))
		return string(details)
	}
	runPostgres("pg_dump", "--file=/tmp/seed.dump", "--format=custom", "--no-owner", "--no-privileges", "--exclude-table-data=public.operation_logs", "--exclude-table-data=public.remember_attempt_diagnostics", "--exclude-table-data=public.remember_invocation_diagnostics", "--exclude-table-data=public.remember_failure_artifacts")
	dump, err := client.CopyFileFromContainer(ctx, "/tmp/seed.dump")
	require.NoError(t, err)
	dumpPath := filepath.Join(manifest.SeedDir, "database.dump")
	file, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = io.Copy(file, dump)
	readCloseErr, writeCloseErr := dump.Close(), file.Close()
	require.NoError(t, err)
	require.NoError(t, readCloseErr)
	require.NoError(t, writeCloseErr)
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
	t.Cleanup(func() {
		_, dropErr := f.native.DB.Exec(`DROP DATABASE "` + target + `" WITH (FORCE)`)
		require.NoError(t, dropErr)
	})
	entries := strings.Split(runPostgres("pg_restore", "--list", "/tmp/seed.dump"), "\n")
	spaces := 0
	for index, entry := range entries {
		if strings.Contains(entry, " TABLE DATA public memory_spaces ") {
			entries[index] = ";" + entry
			spaces++
		}
	}
	require.Equal(t, 1, spaces)
	require.NoError(t, client.CopyToContainer(ctx, []byte(strings.Join(entries, "\n")), "/tmp/seed-restore.list", 0600))
	command := []string{"pg_restore", "--dbname", target, "--no-owner", "--no-privileges", "--exit-on-error", "--jobs=2"}
	runPostgres(append(append([]string{}, command...), "--section=pre-data", "--clean", "--if-exists", "/tmp/seed.dump")...)
	runPostgres(append(append([]string{}, command...), "--data-only", "--schema=public", "--table=memory_spaces", "/tmp/seed.dump")...)
	runPostgres(append(append([]string{}, command...), "--section=data", "--section=post-data", "--use-list=/tmp/seed-restore.list", "/tmp/seed.dump")...)
	config.Database = target
	restoredDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { require.NoError(t, restoredDB.Close()) })
	restored, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: restoredDB}), &gorm.Config{})
	require.NoError(t, err)
	var restoredDatabase string
	require.NoError(t, restoredDB.QueryRowContext(ctx, "SELECT current_database()").Scan(&restoredDatabase))
	require.Equal(t, target, restoredDatabase)
	require.NoError(t, restoredDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=$1 AND is_applied)", newest).Scan(&applied))
	require.False(t, applied, "the restored seed must require the supported startup migration")
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
