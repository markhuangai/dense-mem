//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.True(t, exists)
}
