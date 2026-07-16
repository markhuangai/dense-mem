//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type profileRepoTestConfig struct {
	dsn string
}

func (c *profileRepoTestConfig) GetPostgresDSN() string {
	return c.dsn
}
func (c *profileRepoTestConfig) GetPostgresReadDSN() string { return "" }

func setupProfileRepositoryTest(t *testing.T, ctx context.Context) (*ProfileRepositoryImpl, func()) {
	t.Helper()

	dsn := postgres.GetTestDSN()
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set, skipping integration test")
	}

	db, err := postgres.Open(ctx, &profileRepoTestConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	require.NoError(t, m.RunUp(ctx), "RunUp should succeed")

	rls := postgres.NewRLS()
	cleanupProfiles := func() {
		_ = rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
			if err := tx.Exec(`
				DELETE FROM api_keys
				WHERE profile_id IN (
					SELECT id FROM profiles WHERE name LIKE 'Test Profile Repository %'
				)
			`).Error; err != nil {
				return err
			}
			return tx.Exec("DELETE FROM profiles WHERE name LIKE 'Test Profile Repository %'").Error
		})
	}
	cleanupProfiles()

	cleanup := func() {
		cleanupProfiles()
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}

	return NewProfileRepository(db, rls), cleanup
}

func TestProfileRepositoryJSONBMapsRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupProfileRepositoryTest(t, ctx)
	defer cleanup()

	profile := &domain.Profile{
		Name:        "Test Profile Repository JSONB Round Trip",
		Description: "profile repository jsonb scan test",
		Metadata: map[string]any{
			"source": "test",
			"nested": map[string]any{
				"enabled": true,
			},
		},
		Config: map[string]any{
			"mode": "strict",
			"features": map[string]any{
				"audit": true,
			},
		},
	}

	require.NoError(t, repo.Create(ctx, profile), "Create should succeed")

	got, err := repo.GetByID(ctx, profile.ID)
	require.NoError(t, err, "GetByID should succeed")
	require.NotNil(t, got, "GetByID should find profile")
	assert.Equal(t, profile.Metadata, got.Metadata)
	assert.Equal(t, profile.Config, got.Config)

	profile.Metadata = map[string]any{"updated": true}
	profile.Config = map[string]any{"mode": "updated"}
	require.NoError(t, repo.Update(ctx, profile), "Update should succeed")

	got, err = repo.GetByID(ctx, profile.ID)
	require.NoError(t, err, "GetByID after update should succeed")
	require.NotNil(t, got, "GetByID after update should find profile")
	assert.Equal(t, profile.Metadata, got.Metadata)
	assert.Equal(t, profile.Config, got.Config)

	list, err := repo.List(ctx, 100, 0)
	require.NoError(t, err, "List should succeed")
	var listed *domain.Profile
	for _, candidate := range list {
		if candidate.ID == profile.ID {
			listed = candidate
			break
		}
	}
	require.NotNil(t, listed, "List should include created profile")
	assert.Equal(t, profile.Metadata, listed.Metadata)
	assert.Equal(t, profile.Config, listed.Config)
}

func TestProfileRepositoryNilJSONBMapsReadAsEmptyObjects(t *testing.T) {
	ctx := context.Background()
	repo, cleanup := setupProfileRepositoryTest(t, ctx)
	defer cleanup()

	profile := &domain.Profile{
		Name:        "Test Profile Repository Empty JSONB",
		Description: "profile repository empty jsonb scan test",
	}
	require.NoError(t, repo.Create(ctx, profile), "Create should succeed")

	got, err := repo.GetByID(ctx, profile.ID)
	require.NoError(t, err, "GetByID should succeed")
	require.NotNil(t, got, "GetByID should find profile")
	assert.Equal(t, map[string]any{}, got.Metadata)
	assert.Equal(t, map[string]any{}, got.Config)
}
