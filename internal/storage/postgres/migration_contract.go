package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var migrationFilenamePattern = regexp.MustCompile(`^((?:\d{10}|\d{14}))_[a-z0-9][a-z0-9_-]*\.sql$`)

// MigrationFile is the immutable identity and checksum of one executable
// Goose migration.
type MigrationFile struct {
	Version  int64
	Filename string
	SHA256   string
}

// ListMigrationFiles returns executable migrations in strict version order.
// Release directories are organizational only; all SQL files form one Goose
// history keyed by their numeric version.
func ListMigrationFiles(dir string) ([]MigrationFile, error) {
	files := make([]MigrationFile, 0)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		filename := filepath.ToSlash(relative)
		name := filepath.Base(filename)
		match := migrationFilenamePattern.FindStringSubmatch(name)
		if match == nil {
			return fmt.Errorf("migration history: malformed executable filename %q", filename)
		}
		version, parseErr := strconv.ParseInt(match[1], 10, 64)
		if parseErr != nil || version <= 0 {
			return fmt.Errorf("migration history: invalid version in %q", filename)
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("migration history: read %q: %w", filename, readErr)
		}
		if !bytesContainGooseSections(contents) {
			return fmt.Errorf("migration history: %q must contain Goose Up and Down sections", filename)
		}
		digest := sha256.Sum256(contents)
		files = append(files, MigrationFile{Version: version, Filename: filename, SHA256: hex.EncodeToString(digest[:])})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("migration history: read directory: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Version < files[j].Version })
	for i := 1; i < len(files); i++ {
		if files[i-1].Version == files[i].Version {
			return nil, fmt.Errorf("migration history: duplicate version %d (%s and %s)", files[i].Version, files[i-1].Filename, files[i].Filename)
		}
	}
	return files, nil
}

func bytesContainGooseSections(contents []byte) bool {
	text := string(contents)
	return strings.Contains(text, "-- +goose Up") && strings.Contains(text, "-- +goose Down")
}

// ValidateMigrationHistory validates the current executable history and, when
// a base directory is supplied, rejects edits, deletions, basename renames, or
// versions that do not advance beyond the base history. Release-directory
// relocation is allowed so histories can be organized without changing Goose
// version identity.
func ValidateMigrationHistory(dir, baseDir string) error {
	current, err := ListMigrationFiles(dir)
	if err != nil {
		return err
	}
	if len(current) == 0 {
		return errors.New("migration history: no executable migrations found")
	}
	if strings.TrimSpace(baseDir) == "" {
		return nil
	}
	base, err := ListMigrationFiles(baseDir)
	if err != nil {
		return err
	}
	baseByVersion := make(map[int64]MigrationFile, len(base))
	baseMax := int64(0)
	for _, migration := range base {
		baseByVersion[migration.Version] = migration
		if migration.Version > baseMax {
			baseMax = migration.Version
		}
	}
	currentByVersion := make(map[int64]MigrationFile, len(current))
	for _, migration := range current {
		currentByVersion[migration.Version] = migration
		if previous, ok := baseByVersion[migration.Version]; ok {
			if previous.SHA256 != migration.SHA256 {
				return fmt.Errorf("migration history: deployed migration version %d was modified", migration.Version)
			}
			if filepath.Base(previous.Filename) != filepath.Base(migration.Filename) {
				return fmt.Errorf("migration history: deployed migration version %d was renamed", migration.Version)
			}
		}
		if _, ok := baseByVersion[migration.Version]; !ok && migration.Version <= baseMax {
			return fmt.Errorf("migration history: new migration %q does not advance beyond base version %d", migration.Filename, baseMax)
		}
	}
	for _, migration := range base {
		if _, ok := currentByVersion[migration.Version]; !ok {
			return fmt.Errorf("migration history: deployed migration version %d was deleted or renamed", migration.Version)
		}
	}
	return nil
}

// LatestMigrationVersion resolves the repository's latest migration without a
// version constant in application logic.
func LatestMigrationVersion(dir string) (int64, error) {
	files, err := ListMigrationFiles(dir)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, errors.New("migration history: no migrations")
	}
	return files[len(files)-1].Version, nil
}

func migrationPath(dir string, version int64) (string, error) {
	files, err := ListMigrationFiles(dir)
	if err != nil {
		return "", err
	}
	for _, migration := range files {
		if migration.Version == version {
			return filepath.Join(dir, filepath.FromSlash(migration.Filename)), nil
		}
	}
	return "", fmt.Errorf("migration history: version %d not found", version)
}

// MigrationManifestLine renders a stable tab-separated manifest row.
func MigrationManifestLine(migration MigrationFile) string {
	return fmt.Sprintf("%d\t%s\t%s", migration.Version, migration.Filename, migration.SHA256)
}
