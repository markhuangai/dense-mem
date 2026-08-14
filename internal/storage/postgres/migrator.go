package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	gooselock "github.com/pressly/goose/v3/lock"
	"gorm.io/gorm"
)

// Migrator handles database migrations using goose.
type Migrator struct {
	db  *sql.DB
	dir string
}

// MigratorClient is the companion interface for Migrator.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type MigratorClient interface {
	RunUp(ctx context.Context) error
	RunDown(ctx context.Context) error
	Status(ctx context.Context) error
}

// Ensure Migrator implements MigratorClient
var _ MigratorClient = (*Migrator)(nil)

// getMigrationsDir returns the absolute path to the migrations directory.
// It checks multiple strategies to find the migrations:
// 1. If running from project root (migrations/postgres exists relative to cwd)
// 2. If running from a subdirectory, walks up to find project root
// 3. Falls back to hardcoded absolute path
func getMigrationsDir() string {
	relDir := "migrations/postgres"

	// Strategy 1: Check if migrations exists relative to current working directory
	if _, err := os.Stat(relDir); err == nil {
		absPath, _ := filepath.Abs(relDir)
		return absPath
	}

	// Strategy 2: Walk up directories to find project root
	cwd, _ := os.Getwd()
	for {
		migrationsPath := filepath.Join(cwd, "migrations/postgres")
		if _, err := os.Stat(migrationsPath); err == nil {
			return migrationsPath
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			break // reached root
		}
		cwd = parent
	}

	// Strategy 3: Fallback to absolute path
	return "/app/dense-mem/migrations/postgres"
}

// MigrationsDir returns the executable Goose migration directory used by the
// runtime and migration contract checks.
func MigrationsDir() string { return getMigrationsDir() }

// NewMigrator creates a new migrator instance for the given GORM database.
// Migrations are loaded from the migrations/postgres directory.
func NewMigrator(db *gorm.DB) (*Migrator, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	return &Migrator{
		db:  sqlDB,
		dir: getMigrationsDir(),
	}, nil
}

// NewMigratorWithDB creates a migrator from a sql.DB directly.
func NewMigratorWithDB(sqlDB *sql.DB) *Migrator {
	return &Migrator{
		db:  sqlDB,
		dir: getMigrationsDir(),
	}
}

// RunUp runs all pending up migrations.
func (m *Migrator) RunUp(ctx context.Context) error {
	locker, err := gooselock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("failed to create postgres migration lock: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		m.db,
		os.DirFS(m.dir),
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("failed to create migration provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("failed to run up migrations: %w", err)
	}

	return nil
}

// RunDown runs all down migrations (rollback).
func (m *Migrator) RunDown(ctx context.Context) error {
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	if err := goose.DownContext(ctx, m.db, m.dir); err != nil {
		return fmt.Errorf("failed to run down migrations: %w", err)
	}

	return nil
}

// Status displays the current migration status.
func (m *Migrator) Status(ctx context.Context) error {
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	// goose.Status doesn't have a context variant, use the standard version
	if err := goose.Status(m.db, m.dir); err != nil {
		return fmt.Errorf("failed to get migration status: %w", err)
	}

	return nil
}

// RunUp is a standalone function that runs up migrations on the given database.
// This is the primary export for external use.
func RunUp(ctx context.Context, db *gorm.DB) error {
	m, err := NewMigrator(db)
	if err != nil {
		return err
	}
	return m.RunUp(ctx)
}

// RunDown is a standalone function that runs down migrations on the given database.
// This is the primary export for external use.
func RunDown(ctx context.Context, db *gorm.DB) error {
	m, err := NewMigrator(db)
	if err != nil {
		return err
	}
	return m.RunDown(ctx)
}

// RunStatus is a standalone function that displays migration status.
func RunStatus(ctx context.Context, db *gorm.DB) error {
	m, err := NewMigrator(db)
	if err != nil {
		return err
	}
	return m.Status(ctx)
}

// EnsureMigrationsDir creates the migrations directory if it doesn't exist.
func EnsureMigrationsDir() error {
	dir := "migrations/postgres"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}
