//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/testfixture"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestBootstrapSeedIdentityAndCredentialRejection(t *testing.T) {
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("registered disposable PostgreSQL execution required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const password = "synthetic-bootstrap-database-password-782"
	alias := "seed-auth-" + uuid.NewString()
	options := []testcontainers.ContainerCustomizer{postgres.WithDatabase("densemem"), postgres.WithUsername("densemem"), postgres.WithPassword(password), testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute))}
	resourceRunID := "resource-" + uuid.NewString()
	options = append(options, testcontainers.WithName("densemem-eval-"+resourceRunID+"-postgres"), testcontainers.WithLabels(map[string]string{"io.dense-mem.eval.run": resourceRunID}))
	network := os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK")
	if network != "" {
		options = append(options, tcnetwork.WithNetworkName([]string{alias}, network))
	}
	container, err := testfixture.Run(ctx, "pgvector/pgvector:0.8.2-pg18-trixie", options...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	if network != "" {
		dsn = (&url.URL{Scheme: "postgres", User: url.UserPassword("densemem", password), Host: alias + ":5432", Path: "/densemem", RawQuery: "sslmode=disable"}).String()
	}
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, storagepostgres.NewMigratorWithDB(pool).RunUp(ctx))
	team := uuid.New()
	rls := storagepostgres.NewRLS()
	require.NoError(t, accesspostgres.NewTeamRepository(db, rls).Create(ctx, &domain.Team{ID: team, Name: "seed-bootstrap"}))
	const key = "dm_seed_bootstrap_fixed_key_canary_782"
	manifest := evalharness.Manifest{Dataset: "bootstrap", SeedDir: t.TempDir()}
	for name, value := range map[string]string{"EVAL_TEAM_ID": team.String(), "EVAL_DATABASE_URL": dsn, "EVAL_API_KEY": key, "EVAL_CREDENTIAL_ID": ""} {
		t.Setenv(name, value)
	}
	readScope := func() evalharness.Scope {
		t.Helper()
		file := filepath.Join(manifest.SeedDir, "scope.json")
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		require.NotContains(t, string(body), key)
		require.NotContains(t, string(body), password)
		info, err := os.Stat(file)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		var scope evalharness.Scope
		require.NoError(t, json.Unmarshal(body, &scope))
		return scope
	}
	require.NoError(t, bootstrapCredential(ctx, manifest))
	first := readScope()
	t.Setenv("EVAL_CREDENTIAL_ID", first.CredentialID)
	require.NoError(t, bootstrapCredential(ctx, manifest))
	second := readScope()
	require.Equal(t, first, second, "team, credential, owner, space and generation remain fixed")
	repository := accesspostgres.NewCredentialRepository(db, rls, nil)
	before, err := repository.GetByIDForTeam(ctx, team, uuid.MustParse(first.CredentialID))
	require.NoError(t, err)
	for _, invalid := range []struct {
		team            uuid.UUID
		credential, key string
	}{
		{team, first.CredentialID, "dm_seed_bootstrap_fixed_key_canary_wrong"},
		{team, uuid.NewString(), key},
		{uuid.New(), first.CredentialID, key},
	} {
		t.Setenv("EVAL_TEAM_ID", invalid.team.String())
		t.Setenv("EVAL_CREDENTIAL_ID", invalid.credential)
		t.Setenv("EVAL_API_KEY", invalid.key)
		require.ErrorContains(t, bootstrapCredential(ctx, manifest), "does not match")
		require.Equal(t, first, readScope(), "rejection cannot replace the preserved scope file")
	}
	after, err := repository.GetByIDForTeam(ctx, team, uuid.MustParse(first.CredentialID))
	require.NoError(t, err)
	require.Equal(t, before, after, "rejected bootstrap credentials cannot replace or mutate the existing identity")
	var count int
	require.NoError(t, pool.QueryRowContext(ctx, "SELECT count(*) FROM credentials").Scan(&count))
	require.Equal(t, 1, count)
	t.Run("resource monitoring emits initial periodic and final protected samples and joins", func(t *testing.T) {
		var output bytes.Buffer
		periodic := make(chan struct{}, 1)
		logger := observability.NewConsoleWithHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
				if attr.Key == "payload" {
					data, err := json.Marshal(attr.Value.Any())
					var payload struct {
						Phase string `json:"phase"`
					}
					if err == nil && json.Unmarshal(data, &payload) == nil && payload.Phase == "periodic" {
						select {
						case periodic <- struct{}{}:
						default:
						}
					}
				}
				return attr
			},
		}))
		capture := &evalharness.Capture{Logger: logger.ContextSlog(), Protector: evalharness.NewRunnerCredentialProtector(key, password, dsn), RunID: "resource-fixture"}
		monitorCtx, stop := context.WithCancel(ctx)
		defer stop()
		resourceManifest := evalharness.Manifest{RunID: resourceRunID, SeedDir: manifest.SeedDir}
		finish := startResourceMonitor(monitorCtx, stop, pool, resourceManifest, capture)
		timer := time.NewTimer(90 * time.Second)
		defer timer.Stop()
		observed := false
		select {
		case <-periodic:
			observed = true
		case <-timer.C:
		case <-ctx.Done():
		}
		require.NoError(t, finish())
		require.True(t, observed, "resource monitor must emit a periodic event before its bounded deadline")
		var phases []string
		for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
			var event struct {
				Message string           `json:"msg"`
				Payload resourceSnapshot `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(line, &event))
			require.Equal(t, "eval_resources", event.Message)
			require.NotNil(t, event.Payload.PGConnections)
			require.Equal(t, "not_exposed_by_production_runtime", event.Payload.Unavailable["application_pool_waits"])
			require.Equal(t, "ownership_unverified", event.Payload.Unavailable["server"])
			if event.Payload.Phase == "periodic" {
				require.NotNil(t, event.Payload.HostCPUPercent)
				require.GreaterOrEqual(t, event.Payload.IntervalMS, int64(60_000))
			}
			phases = append(phases, event.Payload.Phase)
		}
		require.Equal(t, []string{"initial", "periodic", "final"}, phases)
		for _, secret := range []string{key, password, dsn} {
			require.NotContains(t, output.String(), secret)
		}
		output.Reset()
		stoppedCtx, cancelMonitor := context.WithCancel(ctx)
		cancelMonitor()
		finish = startResourceMonitor(stoppedCtx, cancelMonitor, pool, resourceManifest, capture)
		require.NoError(t, finish())
		require.Contains(t, output.String(), `"phase":"final"`)
		brokenCtx, cancelBroken := context.WithCancel(ctx)
		defer cancelBroken()
		finish = startResourceMonitor(brokenCtx, cancelBroken, pool, resourceManifest, &evalharness.Capture{})
		require.Error(t, finish())
		require.ErrorIs(t, brokenCtx.Err(), context.Canceled)
		closed, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		require.NoError(t, closed.Close())
		sampler := resourceSampler{}
		unavailable := sampler.sample(ctx, closed, "not-owned", filepath.Join(t.TempDir(), "missing"), "final")
		require.Nil(t, unavailable.PGConnections)
		require.Equal(t, "query_unavailable", unavailable.Unavailable["postgres_activity"])
		require.Equal(t, "counters_unavailable", unavailable.Unavailable["disk"])
	})
}
