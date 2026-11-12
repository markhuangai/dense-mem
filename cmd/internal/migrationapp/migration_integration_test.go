//go:build integration

package migrationapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/testfixture"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/gorm"
)

var migrationWorkingDirectoryMu sync.Mutex

const (
	migrationTestSetupTimeout     = 30 * time.Second
	migrationTestOperationTimeout = 5 * time.Minute
	migrationTestSafetyTimeout    = 30 * time.Second
	migrationTestLockProbeTimeout = time.Second
)

func TestEvidenceOnlyPostgresMigrationDiagnostics(t *testing.T) {
	db := openMigrationIntegrationDB(t)
	ctx := context.Background()
	migrator, err := storagepostgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(ctx))
	const secret = "credential_canary_731"
	const dependency = "evidence_only_credential_canary_731"
	require.NoError(t, db.Exec(`CREATE TABLE `+dependency+` (team_id uuid,relationship_id uuid,FOREIGN KEY(team_id,relationship_id) REFERENCES relationship_records(team_id,relationship_id))`).Error)
	path := filepath.Join(t.TempDir(), "migration.jsonl")
	output, err := os.Create(path)
	require.NoError(t, err)
	previous := os.Stdout
	os.Stdout = output
	logger := observability.NewWithProtector(slog.LevelDebug, observability.NewCredentialProtector(secret))
	os.Stdout = previous
	t.Cleanup(func() { _ = output.Close() })
	require.ErrorContains(t, migrateEvidenceOnly(ctx, db, migrator, logger.Slog()), "unhandled retained dependency")
	require.NoError(t, db.Exec(`DROP TABLE `+dependency).Error)
	require.NoError(t, migrateEvidenceOnly(ctx, db, migrator, logger.Slog()))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, migrateEvidenceOnly(cancelled, db, migrator, logger.Slog()))
	require.NoError(t, output.Close())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), secret)
	require.Contains(t, string(body), "database_detail_omitted")
	seen := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		require.Equal(t, storagepostgres.EvidenceOnlyMigrationInventorySHA256, record["inventory_identity"])
		seen[record["msg"].(string)] = record
	}
	require.Contains(t, seen, "evidence_only_migration_started")
	blocked := seen["evidence_only_migration_blocked"]
	require.Equal(t, "ERROR", blocked["level"])
	require.Contains(t, blocked["error"], "unhandled retained dependency")
	require.Equal(t, "P0001", blocked["sqlstate"])
	require.Equal(t, true, blocked["database_detail_omitted"])
	require.Equal(t, "dependency_check", blocked["phase"])
	completed := seen["evidence_only_migration_completed"]
	require.Equal(t, "INFO", completed["level"])
	require.Equal(t, "committed", completed["transaction_outcome"])
	receipt := completed["receipt"].(map[string]any)
	require.Equal(t, true, receipt["retained_fingerprints_verified"])
	require.Len(t, receipt["retained_counts"], 13)
	require.NotEmpty(t, receipt["removed_counts"])
	failed := seen["evidence_only_migration_failed"]
	require.Equal(t, "ERROR", failed["level"])
	require.Equal(t, "prepare", failed["phase"])
	require.Equal(t, "not_started", failed["transaction_outcome"])
}

func TestEvidenceOnlyPostgresMigrationRollbackLogs(t *testing.T) {
	for _, mode := range []string{"legal_hold", "fingerprint", "interruption"} {
		t.Run(mode, func(t *testing.T) {
			db := openMigrationIntegrationDB(t)
			ctx := context.Background()
			rls := storagepostgres.NewRLS()
			migrator, err := storagepostgres.NewMigrator(db)
			require.NoError(t, err)
			require.NoError(t, migrator.PrepareEvidenceOnlyMigration(ctx))
			team := &domain.Team{Name: "migration-rollback-" + mode}
			require.NoError(t, access.NewTeamRepository(db, rls).Create(ctx, team))
			owner := &domain.Credential{ID: uuid.New(), TeamID: team.ID, Name: "migration owner", KeyHash: "synthetic", KeyPrefix: uuid.NewString()[:20], KeySuffix: "test", Scopes: []string{"read", "write"}}
			require.NoError(t, access.NewCredentialRepository(db, rls, nil).CreateCredential(ctx, owner))
			var space string
			var generation int64
			require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
				return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='private'`, team.ID).Row().Scan(&space, &generation)
			}))
			const content = "Exact rollback Evidence. café 🧭"
			ingest, err := knowledgepostgres.NewStore(db, rls).CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team.ID.String(), OwnerProfileID: owner.ID.String(), SpaceID: space, SpaceGeneration: generation, IdempotencyKey: "original-operation", RequestHash: "original-hash", Evidence: []knowledge.EvidenceInput{{Content: content}}})
			require.NoError(t, err)
			id := ingest.Evidence[0].FragmentID
			var before string
			require.NoError(t, db.Raw(`SELECT md5(to_jsonb(evidence)::text) FROM evidence_fragments evidence WHERE fragment_id=?::uuid`, id).Row().Scan(&before))
			if mode == "legal_hold" {
				require.NoError(t, rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
					return tx.Exec(`INSERT INTO private_memory_legal_holds(team_id,space_id,reason_code,actor_class) VALUES(?::uuid,?::uuid,'migration_test','control')`, team.ID, space).Error
				}))
			}
			if mode == "fingerprint" {
				root := t.TempDir()
				source := storagepostgres.MigrationsDir()
				files, err := filepath.Glob(filepath.Join(source, "*", "*.sql"))
				require.NoError(t, err)
				for _, file := range files {
					relative, err := filepath.Rel(source, file)
					require.NoError(t, err)
					destination := filepath.Join(root, "migrations", "postgres", relative)
					body, err := os.ReadFile(file)
					require.NoError(t, err)
					if filepath.Base(file) == "20261110010000_evidence_only_memory.sql" {
						fault := fmt.Sprintf("ALTER TABLE evidence_fragments DISABLE TRIGGER USER; UPDATE evidence_fragments SET content='controlled corruption' WHERE fragment_id='%s'::uuid;\n", id)
						body = []byte(strings.Replace(string(body), "-- EVIDENCE_ONLY_REBIND_FINGERPRINTS", fault+"-- EVIDENCE_ONLY_REBIND_FINGERPRINTS", 1))
					}
					require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
					require.NoError(t, os.WriteFile(destination, body, 0600))
				}
				t.Chdir(root)
				migrator, err = storagepostgres.NewMigrator(db)
				require.NoError(t, err)
			}
			path := filepath.Join(t.TempDir(), "migration-rollback.jsonl")
			output, err := os.Create(path)
			require.NoError(t, err)
			defer output.Close()
			previous := os.Stdout
			os.Stdout = output
			logger := observability.NewWithProtector(slog.LevelDebug, observability.NewCredentialProtector("migration-rollback-canary"))
			os.Stdout = previous
			if mode == "interruption" {
				blocker := db.Begin()
				require.NoError(t, blocker.Error)
				defer blocker.Rollback()
				require.NoError(t, blocker.Exec(`LOCK TABLE v2_compatibility_markers IN ACCESS EXCLUSIVE MODE`).Error)
				operation, cancel := context.WithCancel(ctx)
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- migrateEvidenceOnly(operation, db, migrator, logger.Slog()) }()
				require.Eventually(t, func() bool {
					var waiting bool
					require.NoError(t, db.Raw(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='public.v2_compatibility_markers'::regclass AND NOT granted)`).Row().Scan(&waiting))
					return waiting
				}, 30*time.Second, 20*time.Millisecond)
				cancel()
				require.NoError(t, blocker.Rollback().Error)
				require.Error(t, <-done)
			} else {
				expected := "legal hold"
				if mode == "fingerprint" {
					expected = "retained fingerprint mismatch"
				}
				require.ErrorContains(t, migrateEvidenceOnly(ctx, db, migrator, logger.Slog()), expected)
			}
			var after string
			var graphExists, applied bool
			require.NoError(t, db.Raw(`SELECT md5(to_jsonb(evidence)::text) FROM evidence_fragments evidence WHERE fragment_id=?::uuid`, id).Row().Scan(&after))
			require.Equal(t, before, after)
			require.NoError(t, db.Raw(`SELECT to_regclass('public.relationship_records') IS NOT NULL,EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=20261110010000 AND is_applied)`).Row().Scan(&graphExists, &applied))
			require.True(t, graphExists)
			require.False(t, applied)
			require.NoError(t, output.Close())
			body, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NotContains(t, string(body), "migration-rollback-canary")
			failure := map[string]any{}
			for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
				var record map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &record))
				require.NotEqual(t, "evidence_only_migration_completed", record["msg"])
				if record["level"] == "ERROR" {
					failure = record
				}
			}
			require.NotEmpty(t, failure)
			require.NotEmpty(t, failure["phase"])
			require.Equal(t, "rolled_back", failure["transaction_outcome"])
			require.Equal(t, storagepostgres.EvidenceOnlyMigrationInventorySHA256, failure["inventory_identity"])
			if mode == "interruption" {
				require.Contains(t, failure["error"], "cancel")
			} else if mode == "fingerprint" {
				require.Contains(t, failure["error"], "retained fingerprint mismatch")
			} else {
				require.Contains(t, failure["error"], "legal hold")
			}
		})
	}
}

func TestRunUpTimeoutRollsBackBlockedMigrationAndReleasesLock(t *testing.T) {
	for _, timeout := range []time.Duration{0, migrationTestOperationTimeout} {
		t.Run(timeout.String(), func(t *testing.T) {
			db := openMigrationIntegrationDB(t)
			sqlDB, err := db.DB()
			require.NoError(t, err)

			useBlockedMigrationDirectory(t)
			_, err = sqlDB.Exec(`CREATE TABLE migration_timeout_blocker (id integer PRIMARY KEY)`)
			require.NoError(t, err)

			blockerConn, err := sqlDB.Conn(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = blockerConn.Close()
			})
			blockerTx, err := blockerConn.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = blockerTx.Rollback()
			})
			_, err = blockerTx.Exec(`LOCK TABLE migration_timeout_blocker IN ACCESS SHARE MODE`)
			require.NoError(t, err)

			err = runBlockedMigrationUntilDeadline(t, sqlDB, func(parent context.Context) error {
				return RunUp(parent, db, timeout, newRecordingLogger())
			})
			require.Error(t, err)
			require.True(t, errors.Is(err, context.DeadlineExceeded), "migration error = %v, want context deadline exceeded", err)
			require.False(t, migrationTimeoutProbeExists(t, sqlDB), "timed-out migration must roll back its probe table")

			require.NoError(t, blockerTx.Rollback())
			require.NoError(t, blockerConn.Close())
			require.NoError(t, RunUp(context.Background(), db, timeout, newRecordingLogger()))
			require.True(t, migrationTimeoutProbeExists(t, sqlDB), "retry must acquire the migration lock and apply the migration")
		})
	}
}

func TestRunDownTimeoutRollsBackBlockedMigration(t *testing.T) {
	db := openMigrationIntegrationDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)

	useBlockedMigrationDirectory(t)
	_, err = sqlDB.Exec(`CREATE TABLE migration_timeout_blocker (id integer PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, RunUp(context.Background(), db, migrationTestSetupTimeout, newRecordingLogger()))
	require.True(t, migrationTimeoutProbeExists(t, sqlDB), "setup must apply the probe migration")

	blockerConn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = blockerConn.Close()
	})
	blockerTx, err := blockerConn.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = blockerTx.Rollback()
	})
	_, err = blockerTx.Exec(`LOCK TABLE migration_timeout_blocker IN ACCESS SHARE MODE`)
	require.NoError(t, err)

	err = runBlockedMigrationUntilDeadline(t, sqlDB, func(parent context.Context) error {
		return RunDown(parent, db, migrationTestOperationTimeout, newRecordingLogger())
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "migration error = %v, want context deadline exceeded", err)
	require.True(t, migrationTimeoutProbeExists(t, sqlDB), "timed-out rollback must restore its probe table")

	require.NoError(t, blockerTx.Rollback())
	require.NoError(t, blockerConn.Close())
	require.NoError(t, RunDown(context.Background(), db, migrationTestSetupTimeout, newRecordingLogger()))
	require.False(t, migrationTimeoutProbeExists(t, sqlDB), "retry must complete the rollback")
}

func openMigrationIntegrationDB(t testing.TB) *gorm.DB {
	db, _ := openMigrationProgressDB(t)
	return db
}

func openMigrationProgressDB(t testing.TB, extra ...testcontainers.ContainerCustomizer) (*gorm.DB, string) {
	t.Helper()
	ctx := context.Background()
	containerOptions := []testcontainers.ContainerCustomizer{
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30 * time.Second),
		),
	}
	if networkName := strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK")); networkName != "" {
		containerOptions = append(containerOptions, tcnetwork.WithNetworkName([]string{"postgres"}, networkName))
	}
	containerOptions = append(containerOptions, extra...)
	container, err := testfixture.Run(ctx, "pgvector/pgvector:0.8.2-pg18-trixie", containerOptions...)
	if err != nil {
		if os.Getenv("DENSE_MEM_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatalf("Postgres is required for this test run: %v", err)
		}
		t.Skipf("Postgres not available: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	dsn, err := migrationIntegrationDSN(ctx, container)
	require.NoError(t, err)
	db, err := storagepostgres.Open(ctx, migrationProgressTestConfig{dsn})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})
	return db, dsn
}

func migrationIntegrationDSN(ctx context.Context, container *tcpostgres.PostgresContainer) (string, error) {
	if strings.TrimSpace(os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK")) == "" {
		return container.ConnectionString(ctx, "sslmode=disable")
	}
	connectionURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword("testuser", "testpass"),
		Host:   "postgres:5432",
		Path:   "/testdb",
	}
	query := connectionURL.Query()
	query.Set("sslmode", "disable")
	connectionURL.RawQuery = query.Encode()
	return connectionURL.String(), nil
}

func useBlockedMigrationDirectory(t *testing.T) {
	t.Helper()
	// postgres.RunUp discovers migrations from the process working directory.
	migrationWorkingDirectoryMu.Lock()
	t.Cleanup(migrationWorkingDirectoryMu.Unlock)

	originalDirectory, err := os.Getwd()
	require.NoError(t, err)
	temporaryDirectory := t.TempDir()
	migrationsDirectory := filepath.Join(temporaryDirectory, "migrations", "postgres", "v2_5")
	require.NoError(t, os.MkdirAll(migrationsDirectory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDirectory, "2026081501_timeout_probe.sql"), []byte(`-- +goose Up
CREATE TABLE migration_timeout_probe (id integer PRIMARY KEY);
LOCK TABLE migration_timeout_blocker IN ACCESS EXCLUSIVE MODE;
INSERT INTO migration_timeout_probe (id) VALUES (1);

-- +goose Down
DROP TABLE migration_timeout_probe;
LOCK TABLE migration_timeout_blocker IN ACCESS EXCLUSIVE MODE;
`), 0o600))
	require.NoError(t, os.Chdir(temporaryDirectory))
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func migrationTimeoutProbeExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRow(`SELECT to_regclass('public.migration_timeout_probe') IS NOT NULL`).Scan(&exists))
	return exists
}

func runBlockedMigrationUntilDeadline(t *testing.T, db *sql.DB, run func(context.Context) error) error {
	t.Helper()
	parent := newControlledDeadlineContext()
	t.Cleanup(parent.expire)

	result := make(chan error, 1)
	go func() {
		result <- run(parent)
	}()

	waitForBlockedMigrationLock(t, db)
	parent.expire()

	select {
	case err := <-result:
		return err
	case <-time.After(migrationTestSafetyTimeout):
		t.Fatal("migration did not stop after the test deadline expired")
		return nil
	}
}

func waitForBlockedMigrationLock(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(migrationTestSafetyTimeout)
	for {
		probeCtx, cancel := context.WithTimeout(context.Background(), migrationTestLockProbeTimeout)
		var waiting bool
		err := db.QueryRowContext(probeCtx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks AS lock
				JOIN pg_class AS relation ON relation.oid = lock.relation
				WHERE relation.relname = 'migration_timeout_blocker'
					AND lock.mode = 'AccessExclusiveLock'
					AND NOT lock.granted
			)
		`).Scan(&waiting)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			require.NoError(t, err)
		}
		if err == nil && waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("migration did not reach the blocked exclusive lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type controlledDeadlineContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newControlledDeadlineContext() *controlledDeadlineContext {
	return &controlledDeadlineContext{
		Context: context.Background(),
		done:    make(chan struct{}),
	}
}

func (ctx *controlledDeadlineContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (ctx *controlledDeadlineContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx *controlledDeadlineContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (ctx *controlledDeadlineContext) expire() {
	ctx.once.Do(func() {
		close(ctx.done)
	})
}

type migrationProgressTestConfig struct{ dsn string }

func (c migrationProgressTestConfig) GetPostgresDSN() string { return c.dsn }
