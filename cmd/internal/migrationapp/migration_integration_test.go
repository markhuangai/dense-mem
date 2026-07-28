//go:build integration

package migrationapp

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var migrationWorkingDirectoryMu sync.Mutex

func TestRunUpTimeoutRollsBackBlockedMigrationAndReleasesLock(t *testing.T) {
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

	startedAt := time.Now()
	err = RunUp(context.Background(), db, 2*time.Second, newRecordingLogger())
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "migration error = %v, want context deadline exceeded", err)
	require.GreaterOrEqual(t, time.Since(startedAt), time.Second)
	require.False(t, migrationTimeoutProbeExists(t, sqlDB), "timed-out migration must roll back its probe table")

	require.NoError(t, blockerTx.Rollback())
	require.NoError(t, blockerConn.Close())
	require.NoError(t, RunUp(context.Background(), db, 5*time.Second, newRecordingLogger()))
	require.True(t, migrationTimeoutProbeExists(t, sqlDB), "retry must acquire the migration lock and apply the migration")
}

func openMigrationIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx,
		"pgvector/pgvector:0.8.2-pg18-trixie",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		t.Skipf("Postgres not available: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})
	return db
}

func useBlockedMigrationDirectory(t *testing.T) {
	t.Helper()
	// postgres.RunUp discovers migrations from the process working directory.
	migrationWorkingDirectoryMu.Lock()
	t.Cleanup(migrationWorkingDirectoryMu.Unlock)

	originalDirectory, err := os.Getwd()
	require.NoError(t, err)
	temporaryDirectory := t.TempDir()
	migrationsDirectory := filepath.Join(temporaryDirectory, "migrations", "postgres")
	require.NoError(t, os.MkdirAll(migrationsDirectory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(migrationsDirectory, "00001_timeout_probe.sql"), []byte(`-- +goose Up
CREATE TABLE migration_timeout_probe (id integer PRIMARY KEY);
LOCK TABLE migration_timeout_blocker IN ACCESS EXCLUSIVE MODE;
INSERT INTO migration_timeout_probe (id) VALUES (1);
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
