//go:build staging_rehearsal

package migrationapp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
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
