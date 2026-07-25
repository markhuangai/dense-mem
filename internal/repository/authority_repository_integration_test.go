package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestAuthorityRepositoryCommitsFreshV2AuthorityOnlyWhenApplicationDataEmpty(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewAuthorityRepository(appDB, rls)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)

	marker, err := repo.CommitFreshV2Authority(ctx, CommitFreshV2AuthorityInput{
		MarkerVersion: "dense-mem.v2.1.cutover.v1",
		Metadata:      map[string]any{"source": "startup"},
		Now:           now,
	})
	require.NoError(t, err)
	require.Equal(t, domain.V2MigrationMarkerCompatible, marker.Status)
	require.Empty(t, marker.RunID)
	require.Equal(t, true, marker.Metadata["fresh_install"])

	latest, err := repo.GetLatestMarker(ctx)
	require.NoError(t, err)
	require.Equal(t, marker.MarkerID, latest.MarkerID)

	var appVisible int64
	require.NoError(t, appDB.Raw(`SELECT count(*) FROM v2_compatibility_markers`).Scan(&appVisible).Error)
	require.Zero(t, appVisible, "app role without system tx_mode must not read markers")

	var systemVisible int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM v2_compatibility_markers`).Scan(&systemVisible).Error
	}))
	require.EqualValues(t, 1, systemVisible)
}

func TestAuthorityRepositoryBlocksFreshV2AuthorityWhenApplicationDataExists(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewAuthorityRepository(appDB, rls)
	createV2LedgerTeam(t, adminDB, rls, "fresh-block-team")

	_, err := repo.CommitFreshV2Authority(ctx, CommitFreshV2AuthorityInput{
		MarkerVersion: "dense-mem.v2.1.cutover.v1",
		Now:           time.Date(2026, 7, 23, 12, 30, 0, 0, time.UTC),
	})
	require.ErrorIs(t, err, ErrFreshV2AuthorityBlocked)
	require.Contains(t, err.Error(), "teams")
}

func TestAuthorityRepositoryRetainsMigrationAuditReadOnly(t *testing.T) {
	_, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()

	err := rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO v2_migration_runs (
				migration_contract_version, corpus_version, source_kind, state
			) VALUES (
				'cleanup-test', 'corpus-test', 'neo4j', 'running'
			)
		`).Error
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "row-level security")
}
