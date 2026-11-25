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
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"

	"github.com/markhuangai/dense-mem/internal/observability"
	searchapp "github.com/markhuangai/dense-mem/internal/search"
	searchpg "github.com/markhuangai/dense-mem/internal/search/postgres"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	organization "github.com/markhuangai/dense-mem/internal/topics"
	topicpg "github.com/markhuangai/dense-mem/internal/topics/postgres"
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
	require.NoError(t, f.native.Control(ctx, http.MethodPatch, "/config/topic-maintenance", map[string]any{"items": []map[string]string{{"key": domain.AppConfigTopicMaintenanceEnabled, "value": "false"}}}, nil))
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
	f.provider.failures = 0
	corpus := []evalharness.CorpusItem{{SourceDocID: "seed-document", Content: "Project Alpha stores durable memory in PostgreSQL."}}
	originalClient := f.native.Client.Client
	transport := originalClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	discarded := false
	f.native.Client.Client = &http.Client{Transport: seedRoundTripper(func(req *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(req)
		if err != nil || discarded || req.Header.Get("Mcp-Name") != "remember" || response.StatusCode != http.StatusOK {
			return response, err
		}
		_, err = io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		discarded = true
		return nil, io.ErrUnexpectedEOF
	})}
	seed, err := evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	f.native.Client.Client = originalClient
	require.True(t, discarded, "the test loses the completed Remember response before the importer reads it")
	require.Equal(t, 1, f.provider.calls, "the identical retry replays the committed row")
	wrongScope := seed.Scope
	wrongScope.SpaceID = uuid.NewString()
	manifest.ResumeScope = &wrongScope
	_, err = evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.ErrorContains(t, err, "checkpoint database scope differs")
	require.Equal(t, 1, f.provider.calls, "a different scope must not dispatch provider work")
	pinnedScope := seed.Scope
	manifest.ResumeScope = &pinnedScope
	changed := append([]evalharness.CorpusItem(nil), corpus...)
	changed[0].Metadata = map[string]any{"altered": true}
	_, err = evalharness.Prepare(ctx, manifest, changed, f.native.Client, f.native)
	require.ErrorContains(t, err, "checkpoint receipt differs from the original Remember request")
	require.Equal(t, 1, f.provider.calls, "a changed request must not dispatch provider work")
	corpus = append(corpus, evalharness.CorpusItem{SourceDocID: "unfinished-document", Content: "The library opens on Sunday."})
	seed, err = evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	require.Equal(t, 2, f.provider.calls, "resume imports only the uncommitted row")
	require.Equal(t, 2, seed.CorpusRows)
	_, err = evalharness.Prepare(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	require.Equal(t, 2, f.provider.calls, "a completed checkpoint can export without Remember calls")
	manifest.ResumeScope = nil
	migrator := storagepostgres.NewMigratorWithDB(f.native.DB)
	const validationVersion = int64(20261124010004)
	require.Equal(t, validationVersion, seed.SchemaVersion, "the fixture rolls back only the no-op Down of the check validation")
	require.NoError(t, migrator.RunDown(ctx))
	var applied bool
	require.NoError(t, f.native.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=$1 AND is_applied)", validationVersion).Scan(&applied))
	require.False(t, applied)
	require.NoError(t, f.native.DB.QueryRowContext(ctx, "SELECT COALESCE(max(version_id),0) FROM (SELECT DISTINCT ON (version_id) version_id,is_applied FROM goose_db_version ORDER BY version_id,id DESC) latest WHERE is_applied").Scan(&seed.SchemaVersion))
	require.Less(t, seed.SchemaVersion, validationVersion)
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
	seedPath := filepath.Join(manifest.SeedDir, "seed.json")
	require.NoError(t, os.WriteFile(seedPath, body, 0600))
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
	require.NoError(t, restoredDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=$1 AND is_applied)", validationVersion).Scan(&applied))
	require.False(t, applied, "the restored seed must require the supported startup migration")
	require.NoError(t, storagepostgres.NewMigratorWithDB(restoredDB).RunUp(ctx))
	for _, table := range []string{"operation_logs", "remember_attempt_diagnostics", "remember_invocation_diagnostics", "remember_failure_artifacts"} {
		var count int
		require.NoError(t, restoredDB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	require.NoError(t, restoredDB.QueryRowContext(ctx, "SELECT is_applied FROM goose_db_version WHERE version_id=$1 ORDER BY id DESC LIMIT 1", validationVersion).Scan(&applied))
	require.True(t, applied)
	loaded, err := accesspostgres.NewCredentialRepository(restored, storagepostgres.NewRLS(), nil).GetActiveByPrefix(ctx, crypto.GetKeyPrefix(key))
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, credential.ID, loaded.ID)
	require.Equal(t, credential.MembershipID, loaded.MembershipID)
	require.Equal(t, credential.MemorySpaceID, loaded.MemorySpaceID)
	require.True(t, crypto.VerifyKey(key, loaded.KeyHash))
	require.False(t, crypto.VerifyKey("wrong-seed-key", loaded.KeyHash))
	rls := storagepostgres.NewRLS()
	restoredSettings := settings.NewAppConfigService(settingspg.NewAppConfigRepository(restored, rls), nil)
	restoredMaintenance := buildTopicMaintenance(topicpg.NewStore(restored, rls), restoredSettings, nil,
		assessor.DefaultSemanticAssessmentLimits(), manifest.Model, 0, 0, nil, nil, organization.MaintenanceDiagnosticCapture{})
	restoredControl := runnerControlServer(t, restoredMaintenance, restoredSettings, searchapp.NewSearchConvergenceService(
		searchpg.NewStore(restored, rls), searchpg.NewStore(restored, rls),
	))
	native := *f.native
	native.DB, native.ControlURL = restoredDB, restoredControl.URL
	beforeCalls := f.provider.calls
	_, _, _, cleanObservation, err := evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.NoError(t, err)
	require.Empty(t, cleanObservation.TopicMaintenance.ConfigurationError)
	require.Equal(t, beforeCalls, f.provider.calls, "restoring and verifying a seed performs no Remember provider work")
	require.Equal(t, seed.Scope, native.Scope)
	setRestoredOntology := func(value string) {
		t.Helper()
		_, err := restoredSettings.UpdateTopicMaintenanceSettings(ctx, map[string]string{domain.AppConfigTopicMaintenanceEnabled: value}, "control", "", "")
		require.NoError(t, err)
	}
	setRestoredOntology("true")
	_, _, _, restoredObservation, err := evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.ErrorContains(t, err, "unexpected native Topic maintenance state")
	require.True(t, restoredObservation.TopicMaintenance.Enabled)
	_, _, _, sourceObservation, err := evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, f.native)
	require.NoError(t, err)
	require.False(t, sourceObservation.TopicMaintenance.Enabled)
	setRestoredOntology("false")
	claimedSkip := seed
	claimedSkip.AISecurityReview = "skipped"
	claimedBody, err := json.Marshal(claimedSkip)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(seedPath, claimedBody, 0600))
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.ErrorContains(t, err, "seed assessment provenance differs")
	require.NoError(t, os.WriteFile(seedPath, body, 0600))
	// Keep the projection converged so VerifySeed must detect the changed vector through its fingerprint.
	changedVector := "[" + strings.TrimSuffix(strings.Repeat("0.5,", manifest.EmbeddingDimensions), ",") + "]"
	require.NoError(t, storagepostgres.NewRLS().WithSystemTx(ctx, restored, func(tx *gorm.DB) error {
		result := tx.Exec(`UPDATE search_documents SET embedding=?::vector WHERE team_id=? AND search_state='current'`, changedVector, native.Scope.TeamID)
		require.Positive(t, result.RowsAffected)
		return result.Error
	}))
	incompatibleSearch, err := native.SearchFingerprint(ctx)
	require.NoError(t, err)
	require.NotEqual(t, seed.SearchHash, incompatibleSearch)
	_, _, _, observation, err := evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.ErrorContains(t, err, "restored search state changed; rebuild the seed")
	require.NoError(t, evalharness.NativeReady(observation, false, true, manifest, native.Scope.TeamID))
	require.Equal(t, seed.CanonicalHash, observation.CanonicalHash)
	require.Equal(t, beforeCalls, f.provider.calls, "an incompatible seed must not trigger Remember provider work")
	afterRejection, err := native.SearchFingerprint(ctx)
	require.NoError(t, err)
	require.Equal(t, incompatibleSearch, afterRejection, "verification must reject incompatible search data without repairing it")
	seed.Dataset = "wrong-dataset"
	body, err = json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(seedPath, body, 0600))
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.ErrorContains(t, err, "dataset")
	seed.Dataset = manifest.Dataset
	seed.Scope.OwnerID = uuid.NewString()
	body, err = json.Marshal(seed)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(seedPath, body, 0600))
	_, _, _, _, err = evalharness.VerifySeed(ctx, manifest, corpus, f.native.Client, &native)
	require.ErrorContains(t, err, "scope differs")
}

type seedRoundTripper func(*http.Request) (*http.Response, error)

func (f seedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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
