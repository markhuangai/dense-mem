//go:build staging_rehearsal

package migrationapp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/cmd/internal/migrationapp"
	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	stagingDatabaseName = "dense-mem"
	stagingPostgresHost = "localhost"
	stagingPostgresPort = uint16(15433)
)

type stagingRehearsalLogger struct {
	t *testing.T
}

func (l stagingRehearsalLogger) Info(message string, _ ...any) {
	l.t.Log(message)
}

func TestStagingMigrationRehearsal(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DENSE_MEM_STAGE_POSTGRES_DSN"))
	if dsn == "" {
		t.Fatal("DENSE_MEM_STAGE_POSTGRES_DSN is required")
	}
	connectionConfig, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal("DENSE_MEM_STAGE_POSTGRES_DSN is invalid; connection details suppressed")
	}
	if connectionConfig.Database != stagingDatabaseName {
		t.Fatalf("staging migration rehearsal refused a DSN for a database other than %q", stagingDatabaseName)
	}
	if err := validateStagingDSN(connectionConfig); err != nil {
		t.Fatal(err)
	}

	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatal("staging migration rehearsal could not open PostgreSQL; connection details suppressed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("staging migration rehearsal could not access PostgreSQL; connection details suppressed")
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	preflightCtx, preflightCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer preflightCancel()
	if err := sqlDB.PingContext(preflightCtx); err != nil {
		t.Fatal("staging migration rehearsal could not reach PostgreSQL; connection details suppressed")
	}
	var databaseName string
	if err := sqlDB.QueryRowContext(preflightCtx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal("staging migration rehearsal could not verify the PostgreSQL database; details suppressed")
	}
	if databaseName != stagingDatabaseName {
		t.Fatalf("staging migration rehearsal refused a database other than %q", stagingDatabaseName)
	}
	if err := storagepostgres.ValidateSinglePrimaryTopology(preflightCtx, db); err != nil {
		t.Fatal("staging migration rehearsal requires a writable single-primary PostgreSQL database")
	}

	migrationCtx, migrationCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer migrationCancel()
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		t.Fatal("staging migration rehearsal could not resolve the staging directory")
	}
	stopCtx, stopCancel := context.WithTimeout(migrationCtx, time.Minute)
	defer stopCancel()
	// The trusted workflow can predate this revision, so stop old workers in the checked-out rehearsal.
	if err := stopStagingServer(stopCtx, filepath.Join(homeDirectory, "dense-mem-stage")); err != nil {
		t.Fatal("staging migration rehearsal could not stop the staging server; details suppressed")
	}
	t.Setenv("POSTGRES_DSN", dsn)
	t.Setenv("POSTGRES_MIGRATION_TIMEOUT_SECONDS", "1800")
	var preflight bytes.Buffer
	if err := serverapp.RunPrivateMemoryPreflight(migrationCtx, &preflight); err != nil {
		t.Fatal("staging migration rehearsal failed private memory preflight; database details suppressed")
	}
	var report privacy.PrivateMemoryMigrationReport
	if err := json.Unmarshal(preflight.Bytes(), &report); err != nil {
		t.Fatal("staging migration rehearsal received an invalid private memory preflight report")
	}
	if report.Status == "complete" {
		if err := migrationapp.RunUp(migrationCtx, db, 30*time.Minute, stagingRehearsalLogger{t: t}); err != nil {
			t.Fatal("staging migration rehearsal failed while applying migrations; database details suppressed")
		}
	} else if err := serverapp.RunPrivateMemoryMigration(migrationCtx, []string{
		"--maintenance-confirmed", "--source-fingerprint", report.SourceFingerprint,
	}, io.Discard); err != nil {
		t.Fatal("staging migration rehearsal failed private memory migration; database details suppressed")
	}

	validationCtx, validationCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer validationCancel()
	if err := storagepostgres.ValidateStartupMigrationState(
		validationCtx,
		sqlDB,
		storagepostgres.MigrationsDir(),
	); err != nil {
		t.Fatal("staging migration rehearsal failed startup migration validation; database details suppressed")
	}
	if err := storagepostgres.CheckPGVectorExtension(validationCtx, db); err != nil {
		t.Fatal("staging migration rehearsal failed pgvector validation; database details suppressed")
	}

	t.Log("staging migrations and startup migration state validation completed")
}

func stopStagingServer(ctx context.Context, directory string) error {
	// Existing Compose identity avoids requiring the image selected for a later deployment.
	output, err := exec.CommandContext(ctx, "docker", "ps", "--no-trunc",
		"--filter", "label=com.docker.compose.project.working_dir="+directory,
		"--filter", "label=com.docker.compose.service=server", "--format", "{{.ID}}").Output()
	if err != nil {
		return err
	}
	containerIDs := strings.Fields(string(output))
	if len(containerIDs) == 0 {
		return nil
	}
	return exec.CommandContext(ctx, "docker", append([]string{"stop", "-t", "30"}, containerIDs...)...).Run()
}

func TestStopStagingServerWithoutImageSelection(t *testing.T) {
	t.Setenv("DENSE_MEM_IMAGE", "")
	compose := func(directory string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, "docker", append([]string{"compose", "--project-directory",
			directory, "--file", filepath.Join(directory, "compose.yaml"),
			"--project-name", filepath.Base(directory)}, args...)...)
		command.Env = append(os.Environ(), "DENSE_MEM_IMAGE=alpine:3.24")
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return strings.TrimSpace(string(output))
	}
	newFixture := func() string {
		t.Helper()
		directory, err := os.MkdirTemp(t.TempDir(), "staging-stop-")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte(`services:
  server:
    image: ${DENSE_MEM_IMAGE:?DENSE_MEM_IMAGE must be set}
    command: ["sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 1; done"]
    network_mode: none
  keeper:
    image: ${DENSE_MEM_IMAGE:?DENSE_MEM_IMAGE must be set}
    command: ["sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 1; done"]
    network_mode: none
`), 0o600))
		t.Cleanup(func() { compose(directory, "down", "--remove-orphans") })
		compose(directory, "up", "-d", "--scale", "server=2")
		return directory
	}
	stagingDirectory, otherDirectory := newFixture(), newFixture()
	stagingServers := strings.Fields(compose(stagingDirectory, "ps", "--all", "--quiet", "server"))
	require.Len(t, stagingServers, 2)
	protected := strings.Fields(compose(stagingDirectory, "ps", "--quiet", "keeper") + "\n" +
		compose(otherDirectory, "ps", "--quiet"))
	require.Len(t, protected, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	assertRunning := func(ids []string, expected string) {
		t.Helper()
		for _, id := range ids {
			output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", id).Output()
			require.NoError(t, err)
			require.Equal(t, expected, strings.TrimSpace(string(output)))
		}
	}
	cancelled, cancelStop := context.WithCancel(ctx)
	cancelStop()
	require.Error(t, stopStagingServer(cancelled, stagingDirectory))
	assertRunning(stagingServers, "true")
	require.NoError(t, stopStagingServer(ctx, stagingDirectory))
	assertRunning(stagingServers, "false")
	assertRunning(protected, "true")
	require.NoError(t, stopStagingServer(ctx, stagingDirectory))
}

func validateStagingDSN(connectionConfig *pgconn.Config) error {
	if connectionConfig == nil {
		return errors.New("staging migration rehearsal could not validate the PostgreSQL DSN")
	}
	if connectionConfig.Host != stagingPostgresHost || connectionConfig.Port != stagingPostgresPort {
		return errors.New("staging migration rehearsal refused a DSN for an unexpected PostgreSQL endpoint")
	}
	for _, fallback := range connectionConfig.Fallbacks {
		if fallback == nil || fallback.Host != stagingPostgresHost || fallback.Port != stagingPostgresPort {
			return errors.New("staging migration rehearsal refused a PostgreSQL DSN with a fallback endpoint")
		}
	}
	return nil
}

func TestValidateStagingDSN(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		wantErr bool
	}{
		{
			name: "single endpoint",
			dsn:  "postgres://user:password@localhost:15433/dense-mem",
		},
		{
			name:    "fallback endpoint",
			dsn:     "postgres://user:password@localhost:15433,prod-db:5432/dense-mem?sslmode=disable",
			wantErr: true,
		},
		{
			name:    "production host",
			dsn:     "postgres://user:password@prod-db:15433/dense-mem",
			wantErr: true,
		},
		{
			name:    "production port",
			dsn:     "postgres://user:password@localhost:5432/dense-mem",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connectionConfig, err := pgconn.ParseConfig(tt.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateStagingDSN(connectionConfig); (err != nil) != tt.wantErr {
				t.Fatalf("validateStagingDSN() error = %v, want error: %t", err, tt.wantErr)
			}
		})
	}

	if err := validateStagingDSN(nil); err == nil {
		t.Fatal("validateStagingDSN(nil) should fail")
	}
}
