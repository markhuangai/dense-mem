//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddingConfigRepository_GetActive_Empty(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	// Clean up any existing embedding_config
	sqlDB.Exec("DELETE FROM embedding_config")

	repo := NewEmbeddingConfigRepository(db)

	// Test GetActive returns nil for empty table
	record, err := repo.GetActive(ctx)
	require.NoError(t, err, "GetActive should succeed")
	assert.Nil(t, record, "GetActive should return nil for empty table")
}

func TestEmbeddingConfigRepository_UpsertAndGetActive(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	// Clean up any existing embedding_config
	sqlDB.Exec("DELETE FROM embedding_config")

	repo := NewEmbeddingConfigRepository(db)

	// Test Upsert
	err = repo.Upsert(ctx, "text-embedding-3-small", 1536)
	require.NoError(t, err, "Upsert should succeed")

	// Test GetActive returns the record
	record, err := repo.GetActive(ctx)
	require.NoError(t, err, "GetActive should succeed")
	require.NotNil(t, record, "GetActive should return a record")
	assert.Equal(t, "text-embedding-3-small", record.Model)
	assert.Equal(t, 1536, record.Dimensions)
	assert.False(t, record.UpdatedAt.IsZero(), "UpdatedAt should be set")

	// Test Upsert updates the record
	time.Sleep(10 * time.Millisecond) // Ensure updated_at changes
	err = repo.Upsert(ctx, "text-embedding-3-large", 3072)
	require.NoError(t, err, "Upsert update should succeed")

	record, err = repo.GetActive(ctx)
	require.NoError(t, err, "GetActive should succeed")
	require.NotNil(t, record, "GetActive should return a record")
	assert.Equal(t, "text-embedding-3-large", record.Model)
	assert.Equal(t, 3072, record.Dimensions)
}
