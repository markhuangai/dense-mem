//go:build integration

package migrationapp

import (
	"os"
	"testing"

	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOntologyUpgradeComposeSeed(t *testing.T) {
	if os.Getenv("DENSE_MEM_E2E_FEATURE_STORAGE_VARIANT") != "" {
		seedFeatureStorageCompose(t)
		return
	}
	variant := os.Getenv("DENSE_MEM_E2E_ONTOLOGY_UPGRADE_VARIANT")
	if variant == "" {
		t.Skip("Topic upgrade seed requires its dedicated production-image scenario")
	}
	require.Contains(t, []string{"v2.8.1", "interrupted"}, variant)
	dsn := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dsn)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	ctx := t.Context()
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(ctx))
	seedProgressMigration(t, db, 210, 0)
	require.NoError(t, migrator.RunUpThroughEvidenceOnly(ctx))
	seedFeatureStorageProgress(t, db, 210)
	if variant == "v2.8.1" {
		require.NoError(t, migrator.RunUpTo(ctx, 20261114010000))
		return
	}
	require.NoError(t, migrator.RunUpTo(ctx, storage.RelationalTopicDataVersion-1))
	require.NoError(t, db.Exec(`CREATE FUNCTION relational_topic_fixture_failure() RETURNS trigger
		LANGUAGE plpgsql AS $fixture$
		BEGIN
			IF (SELECT count(*) FROM topics)>=100 THEN
				RAISE EXCEPTION 'relational_topics_copy_blocked: destination write rejected';
			END IF;
			RETURN NEW;
		END $fixture$;
		CREATE TRIGGER relational_topic_fixture_failure BEFORE INSERT ON topics
		FOR EACH ROW EXECUTE FUNCTION relational_topic_fixture_failure()`).Error)
}
