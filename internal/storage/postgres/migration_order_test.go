//go:build integration

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationFilesystemCombinesReleaseDirectories(t *testing.T) {
	dir := t.TempDir()
	writeMigrationFixture(t, dir, "v2_4/20260101020000_second.sql")
	writeMigrationFixture(t, dir, "v2_5/2026010101_first.sql")

	sources, err := listMigrationSources(dir)
	require.NoError(t, err)
	require.Len(t, sources, 2)
	assert.Equal(t, int64(2026010101), sources[0].version)
	assert.Equal(t, "v2_5/2026010101_first.sql", sources[0].filename)
	assert.Equal(t, int64(20260101020000), sources[1].version)
	assert.Equal(t, "v2_4/20260101020000_second.sql", sources[1].filename)

	filesystem, err := migrationFilesystem(dir)
	require.NoError(t, err)
	assert.Contains(t, filesystem, "2026010101_first.sql")
	assert.Contains(t, filesystem, "20260101020000_second.sql")

	filename, err := migrationPath(dir, 20260101020000)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "v2_4", "20260101020000_second.sql"), filename)
}

func TestMigrationFilesystemRejectsInvalidHistory(t *testing.T) {
	t.Run("duplicate version", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationFixture(t, dir, "v2_4/2026010101_first.sql")
		writeMigrationFixture(t, dir, "v2_5/2026010101_duplicate.sql")
		_, err := migrationFilesystem(dir)
		require.ErrorContains(t, err, "duplicate version")
	})

	t.Run("root migration", func(t *testing.T) {
		dir := t.TempDir()
		writeMigrationFixture(t, dir, "2026010101_root.sql")
		_, err := migrationFilesystem(dir)
		require.ErrorContains(t, err, "versioned release directory")
	})

	t.Run("missing goose section", func(t *testing.T) {
		dir := t.TempDir()
		filename := filepath.Join(dir, "v2_4", "2026010101_incomplete.sql")
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, []byte("-- +goose Up\nSELECT 1;\n"), 0o644))
		_, err := migrationFilesystem(dir)
		require.ErrorContains(t, err, "Goose Up and Down")
	})

	t.Run("directive text in a comment", func(t *testing.T) {
		dir := t.TempDir()
		filename := filepath.Join(dir, "v2_4", "2026010101_comment.sql")
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, []byte("-- +goose Up\nSELECT 1;\n-- Documentation mentions -- +goose Down.\n"), 0o644))
		_, err := migrationFilesystem(dir)
		require.ErrorContains(t, err, "Goose Up and Down")
	})
}

func writeMigrationFixture(t *testing.T, dir, relative string) {
	t.Helper()
	filename := filepath.Join(dir, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	require.NoError(t, os.WriteFile(filename, []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"), 0o644))
}

func TestMigrationOrder_NoConnectorTables(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)

	m, err := NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, m.RunUp(ctx))

	sqlDB, err := db.DB()
	require.NoError(t, err)

	var exists bool
	err = sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='connector_credentials'
		)`).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists)

	err = sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='profiles'
		)`).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists)

	err = sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='api_keys'
		)`).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists)

	err = sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='teams'
		)`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)

	err = sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='team_profiles'
		)`).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists)

	err = sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name='credentials'
		)`).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists)
}
