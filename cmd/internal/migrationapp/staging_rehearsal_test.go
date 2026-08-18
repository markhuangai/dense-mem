//go:build staging_rehearsal

package migrationapp

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const stagingDatabaseName = "dense-mem"

const (
	stagingPostgresHostEnv = "DENSE_MEM_STAGE_POSTGRES_HOST"
	stagingPostgresPortEnv = "DENSE_MEM_STAGE_POSTGRES_PORT"
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
	if err := validateStagingEndpoint(connectionConfig); err != nil {
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

	if err := RunUp(
		context.Background(),
		db,
		30*time.Minute,
		stagingRehearsalLogger{t: t},
	); err != nil {
		t.Fatal("staging migration rehearsal failed while applying migrations; database details suppressed")
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

func validateStagingEndpoint(connectionConfig *pgconn.Config) error {
	if connectionConfig == nil {
		return errors.New("staging migration rehearsal could not validate the PostgreSQL endpoint")
	}
	expectedHost := strings.TrimSpace(os.Getenv(stagingPostgresHostEnv))
	if expectedHost == "" {
		return errors.New(stagingPostgresHostEnv + " is required to prove the target is staging")
	}
	expectedPortText := strings.TrimSpace(os.Getenv(stagingPostgresPortEnv))
	expectedPort, err := strconv.ParseUint(expectedPortText, 10, 16)
	if err != nil || expectedPort == 0 {
		return errors.New(stagingPostgresPortEnv + " must be a valid PostgreSQL port")
	}
	if connectionConfig.Host != expectedHost {
		return errors.New("staging migration rehearsal refused a DSN for an unexpected PostgreSQL host")
	}
	if connectionConfig.Port != uint16(expectedPort) {
		return errors.New("staging migration rehearsal refused a DSN for an unexpected PostgreSQL port")
	}
	for _, fallback := range connectionConfig.Fallbacks {
		if fallback == nil || fallback.Host != expectedHost || fallback.Port != uint16(expectedPort) {
			return errors.New("staging migration rehearsal refused a DSN with an unexpected PostgreSQL fallback endpoint")
		}
	}
	return nil
}

func TestValidateStagingEndpoint(t *testing.T) {
	connectionConfig, err := pgconn.ParseConfig("postgres://user:password@localhost:15433/dense-mem")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(stagingPostgresHostEnv, "localhost")
	t.Setenv(stagingPostgresPortEnv, "15433")
	if err := validateStagingEndpoint(connectionConfig); err != nil {
		t.Fatalf("expected the configured staging endpoint to pass: %v", err)
	}

	multiEndpointConfig, err := pgconn.ParseConfig("postgres://user:password@localhost:15433,prod-db:5432/dense-mem?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if len(multiEndpointConfig.Fallbacks) == 0 {
		t.Fatal("expected the multi-host DSN to contain a fallback endpoint")
	}
	if err := validateStagingEndpoint(multiEndpointConfig); err == nil {
		t.Fatal("expected a production fallback endpoint to be rejected")
	}

	t.Setenv(stagingPostgresPortEnv, "5432")
	if err := validateStagingEndpoint(connectionConfig); err == nil {
		t.Fatal("expected a production-port DSN to be rejected")
	}

	t.Setenv(stagingPostgresPortEnv, "15433")
	t.Setenv(stagingPostgresHostEnv, "db.example")
	if err := validateStagingEndpoint(connectionConfig); err == nil {
		t.Fatal("expected a non-staging host DSN to be rejected")
	}
}
