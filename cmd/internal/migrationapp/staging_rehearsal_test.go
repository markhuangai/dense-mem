//go:build staging_rehearsal

package migrationapp

import (
	"context"
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

const stagingDatabaseName = "dense-mem"

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
