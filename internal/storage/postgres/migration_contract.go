package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// ListMigrationFiles returns the executable migrations in strict version
// order. Nested baseline records are deliberately ignored.
func ListMigrationFiles(dir string) ([]MigrationFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("migration history: read directory: %w", err)
	}
	files := make([]MigrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		match := migrationFilenamePattern.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("migration history: malformed executable filename %q", name)
		}
		version, parseErr := strconv.ParseInt(match[1], 10, 64)
		if parseErr != nil || version <= 0 {
			return nil, fmt.Errorf("migration history: invalid version in %q", name)
		}
		contents, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			return nil, fmt.Errorf("migration history: read %q: %w", name, readErr)
		}
		if !bytesContainGooseSections(contents) {
			return nil, fmt.Errorf("migration history: %q must contain Goose Up and Down sections", name)
		}
		digest := sha256.Sum256(contents)
		files = append(files, MigrationFile{Version: version, Filename: name, SHA256: hex.EncodeToString(digest[:])})
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
// a base directory is supplied, rejects edits, deletions, renames, or versions
// that do not advance beyond the base history.
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
	baseByName := make(map[string]MigrationFile, len(base))
	baseMax := int64(0)
	for _, migration := range base {
		baseByName[migration.Filename] = migration
		if migration.Version > baseMax {
			baseMax = migration.Version
		}
	}
	currentByName := make(map[string]MigrationFile, len(current))
	for _, migration := range current {
		currentByName[migration.Filename] = migration
		if previous, ok := baseByName[migration.Filename]; ok && previous.SHA256 != migration.SHA256 {
			return fmt.Errorf("migration history: deployed migration %q was modified", migration.Filename)
		}
		if _, ok := baseByName[migration.Filename]; !ok && migration.Version <= baseMax {
			return fmt.Errorf("migration history: new migration %q does not advance beyond base version %d", migration.Filename, baseMax)
		}
	}
	for _, migration := range base {
		if _, ok := currentByName[migration.Filename]; !ok {
			return fmt.Errorf("migration history: deployed migration %q was deleted or renamed", migration.Filename)
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

// MigrationManifestLine renders a stable tab-separated manifest row.
func MigrationManifestLine(migration MigrationFile) string {
	return fmt.Sprintf("%d\t%s\t%s", migration.Version, migration.Filename, migration.SHA256)
}
