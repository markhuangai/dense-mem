//go:build integration

package migrationapp

import (
	"context"
	"os"
	"testing"

	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestFeatureStorageComposeSeed(t *testing.T) {
	if os.Getenv("DENSE_MEM_E2E_FEATURE_STORAGE_VARIANT") != "interrupted" {
		t.Skip("feature storage seed requires its dedicated production-image scenario")
	}
	dsn := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dsn)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	migrator, err := storage.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.PrepareEvidenceOnlyMigration(context.Background()))
	seedProgressMigration(t, db, 210, 0)
	require.NoError(t, migrator.RunUpThroughEvidenceOnly(context.Background()))
	require.NoError(t, migrator.PrepareFeatureStorageMigration(context.Background()))
	installFeatureStorageFailure(t, db)
}

func installFeatureStorageFailure(t testing.TB, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE FUNCTION feature_storage_fixture_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF (SELECT count(*) FROM ontology_topic_revision_payloads)>=100 THEN RAISE EXCEPTION 'feature_storage_blocked: destination payload mismatch'; END IF;
	 RETURN NEW; END $$;
	 CREATE TRIGGER feature_storage_fixture_failure BEFORE INSERT ON ontology_topic_revision_payloads FOR EACH ROW EXECUTE FUNCTION feature_storage_fixture_failure()`).Error)
}
