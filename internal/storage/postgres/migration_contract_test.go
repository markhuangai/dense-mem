package postgres

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationHistoryIsStrictAndHasNoDuplicateVersions(t *testing.T) {
	files, err := ListMigrationFiles(filepath.Join("..", "..", "..", "migrations", "postgres"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 83)
	for i := 1; i < len(files); i++ {
		require.Greater(t, files[i].Version, files[i-1].Version)
		require.Len(t, files[i].SHA256, 64)
	}
}

func TestMigrationHistoryRejectsChangedBaseAndOutOfOrderAddition(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	baseSQL := "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"
	require.NoError(t, os.WriteFile(filepath.Join(base, "2026080905_base.sql"), []byte(baseSQL), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(current, "2026080905_base.sql"), []byte(baseSQL+"-- changed\n"), 0o644))
	err := ValidateMigrationHistory(current, base)
	require.Error(t, err)
	require.Contains(t, err.Error(), "modified")

	current = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(current, "2026080905_base.sql"), []byte(baseSQL), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(current, "2026080904_late.sql"), []byte(baseSQL), 0o644))
	err = ValidateMigrationHistory(current, base)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "does not advance") || strings.Contains(err.Error(), "order"))
}

func TestMigrationHistoryAllowsRelocatingAnImmutableVersion(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	baseSQL := "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"
	require.NoError(t, os.Mkdir(filepath.Join(base, "v2_4"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(current, "v2_4"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "v2_4", "2026080905_base.sql"), []byte(baseSQL), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(current, "v2_4", "2026080905_base.sql"), []byte(baseSQL), 0o644))
	require.NoError(t, os.Rename(filepath.Join(current, "v2_4", "2026080905_base.sql"), filepath.Join(current, "2026080905_base.sql")))

	require.NoError(t, ValidateMigrationHistory(current, base))
}

func TestMigrationHistoryRejectsRenamingAnImmutableVersion(t *testing.T) {
	base := t.TempDir()
	current := t.TempDir()
	baseSQL := "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"
	require.NoError(t, os.Mkdir(filepath.Join(base, "v2_4"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(current, "v2_4"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "v2_4", "2026080905_base.sql"), []byte(baseSQL), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(current, "v2_4", "2026080905_renamed.sql"), []byte(baseSQL), 0o644))

	err := ValidateMigrationHistory(current, base)
	require.Error(t, err)
	require.Contains(t, err.Error(), "renamed")
}

func TestMigrationFilesystemCombinesReleaseDirectories(t *testing.T) {
	root := t.TempDir()
	for _, release := range []string{"v2_4", "v2_5"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, release), 0o755))
	}
	contents := "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "v2_4", "2026080905_old.sql"), []byte(contents), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "v2_5", "2026081001_new.sql"), []byte(contents), 0o644))

	fsys, err := migrationFilesystem(root)
	require.NoError(t, err)
	paths, err := fs.Glob(fsys, "*.sql")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"2026080905_old.sql", "2026081001_new.sql"}, paths)
}
