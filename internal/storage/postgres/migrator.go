package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
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

var (
	migrationFilenamePattern = regexp.MustCompile(`^((?:\d{10}|\d{14}))_[a-z0-9][a-z0-9_-]*\.sql$`)
	migrationReleasePattern  = regexp.MustCompile(`^v\d+_\d+$`)
)

type migrationSource struct {
	version  int64
	filename string
	contents []byte
}

func listMigrationSources(dir string) ([]migrationSource, error) {
	sources := make([]migrationSource, 0)
	err := filepath.WalkDir(dir, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("migration history: %q is not a regular file", filename)
		}
		relative, err := filepath.Rel(dir, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		parts := strings.Split(relative, "/")
		if len(parts) != 2 || !migrationReleasePattern.MatchString(parts[0]) {
			return fmt.Errorf("migration history: %q is not inside one versioned release directory", relative)
		}
		match := migrationFilenamePattern.FindStringSubmatch(parts[1])
		if match == nil {
			return fmt.Errorf("migration history: malformed filename %q", relative)
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version <= 0 {
			return fmt.Errorf("migration history: invalid version in %q", relative)
		}
		contents, err := os.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("migration history: read %q: %w", relative, err)
		}
		text := string(contents)
		if !strings.Contains(text, "-- +goose Up") || !strings.Contains(text, "-- +goose Down") {
			return fmt.Errorf("migration history: %q must contain Goose Up and Down sections", relative)
		}
		sources = append(sources, migrationSource{version: version, filename: relative, contents: contents})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("migration history: read directory: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("migration history: no migrations found")
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].version < sources[j].version })
	for i := 1; i < len(sources); i++ {
		if sources[i-1].version == sources[i].version {
			return nil, fmt.Errorf(
				"migration history: duplicate version %d (%s and %s)",
				sources[i].version,
				sources[i-1].filename,
				sources[i].filename,
			)
		}
	}
	return sources, nil
}

func migrationFilesystem(dir string) (fstest.MapFS, error) {
	sources, err := listMigrationSources(dir)
	if err != nil {
		return nil, err
	}
	filesystem := make(fstest.MapFS, len(sources))
	for _, source := range sources {
		name := path.Base(source.filename)
		filesystem[name] = &fstest.MapFile{Data: source.contents, Mode: 0o644}
	}
	return filesystem, nil
}

func migrationPath(dir string, version int64) (string, error) {
	sources, err := listMigrationSources(dir)
	if err != nil {
		return "", err
	}
	for _, source := range sources {
		if source.version == version {
			return filepath.Join(dir, filepath.FromSlash(source.filename)), nil
		}
	}
	return "", fmt.Errorf("migration history: version %d not found", version)
}

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

// NewMigrator creates a new migrator instance for the given GORM database.
// Migrations are loaded as one history across the release directories.
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

func newMigrationProvider(dir string, db *sql.DB, withLock bool) (*goose.Provider, error) {
	if db == nil {
		return nil, fmt.Errorf("migration provider: database is required")
	}
	filesystem, err := migrationFilesystem(dir)
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
	provider, err := goose.NewProvider(goose.DialectPostgres, db, filesystem, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to create migration provider: %w", err)
	}
	return provider, nil
}

func migrationUpTo(ctx context.Context, db *sql.DB, version int64) error {
	provider, err := newMigrationProvider(getMigrationsDir(), db, false)
	if err != nil {
		return err
	}
	_, err = provider.UpTo(ctx, version)
	return err
}

func migrationDownTo(ctx context.Context, db *sql.DB, version int64) error {
	provider, err := newMigrationProvider(getMigrationsDir(), db, false)
	if err != nil {
		return err
	}
	_, err = provider.DownTo(ctx, version)
	return err
}

func migrationDown(ctx context.Context, db *sql.DB) error {
	provider, err := newMigrationProvider(getMigrationsDir(), db, false)
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

// EnsureMigrationsDir creates the migrations directory if it doesn't exist.
func EnsureMigrationsDir() error {
	dir := "migrations/postgres"
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}
