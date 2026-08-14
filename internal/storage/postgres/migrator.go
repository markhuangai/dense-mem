package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"testing/fstest"

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

// getMigrationsDir returns the absolute path to the release-organized
// migrations directory.
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
// Migrations are loaded from all release directories under migrations/postgres.
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

func newMigrationProvider(dir string, sqlDB *sql.DB, withLock bool) (*goose.Provider, error) {
	if sqlDB == nil {
		return nil, fmt.Errorf("migration provider: database is required")
	}
	fsys, err := migrationFilesystem(dir)
	if err != nil {
		return nil, err
	}
	options := make([]goose.ProviderOption, 0, 1)
	if withLock {
		locker, err := gooselock.NewPostgresSessionLocker()
		if err != nil {
			return nil, fmt.Errorf("failed to create postgres migration lock: %w", err)
		}
		options = append(options, goose.WithSessionLocker(locker))
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create migration provider: %w", err)
	}
	return provider, nil
}

// migrationFilesystem presents the release-organized files as one flat Goose
// filesystem. Goose records only numeric versions, so the directory split does
// not change the deployed history or its rollback semantics.
func migrationFilesystem(dir string) (fstest.MapFS, error) {
	files, err := ListMigrationFiles(dir)
	if err != nil {
		return nil, err
	}
	fsys := make(fstest.MapFS, len(files))
	for _, migration := range files {
		name := path.Base(migration.Filename)
		if _, exists := fsys[name]; exists {
			return nil, fmt.Errorf("migration history: duplicate migration filename %q", name)
		}
		contents, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(migration.Filename)))
		if err != nil {
			return nil, fmt.Errorf("migration history: read %q: %w", migration.Filename, err)
		}
		fsys[name] = &fstest.MapFile{Data: contents, Mode: 0o644}
	}
	return fsys, nil
}

func migrationUpTo(ctx context.Context, sqlDB *sql.DB, version int64) error {
	provider, err := newMigrationProvider(getMigrationsDir(), sqlDB, false)
	if err != nil {
		return err
	}
	_, err = provider.UpTo(ctx, version)
	return err
}

func migrationDownTo(ctx context.Context, sqlDB *sql.DB, version int64) error {
	provider, err := newMigrationProvider(getMigrationsDir(), sqlDB, false)
	if err != nil {
		return err
	}
	_, err = provider.DownTo(ctx, version)
	return err
}

func migrationDown(ctx context.Context, sqlDB *sql.DB) error {
	provider, err := newMigrationProvider(getMigrationsDir(), sqlDB, false)
	if err != nil {
		return err
	}
	_, err = provider.Down(ctx)
	return err
}

// RunUp runs all pending up migrations.
func (m *Migrator) RunUp(ctx context.Context) error {
	provider, err := newMigrationProvider(m.dir, m.db, true)
	if err != nil {
		return err
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("failed to run up migrations: %w", err)
	}

	return nil
}

// RunDown runs all down migrations (rollback).
func (m *Migrator) RunDown(ctx context.Context) error {
	provider, err := newMigrationProvider(m.dir, m.db, false)
	if err != nil {
		return err
	}
	if _, err := provider.Down(ctx); err != nil {
		return fmt.Errorf("failed to run down migrations: %w", err)
	}

	return nil
}

// Status displays the current migration status.
func (m *Migrator) Status(ctx context.Context) error {
	provider, err := newMigrationProvider(m.dir, m.db, false)
	if err != nil {
		return err
	}
	if _, err := provider.Status(ctx); err != nil {
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

// EnsureMigrationsDir creates the release migration directories if they don't
// exist.
func EnsureMigrationsDir() error {
	root := "migrations/postgres"
	for _, release := range []string{"v2_4", "v2_5"} {
		if err := os.MkdirAll(filepath.Join(root, release), 0o755); err != nil {
			return err
		}
	}
	return nil
}
