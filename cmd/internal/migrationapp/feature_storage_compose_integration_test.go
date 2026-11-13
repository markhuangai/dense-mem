//go:build integration

package migrationapp

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func seedFeatureStorageCompose(t *testing.T) {
	t.Helper()
	require.Equal(t, "interrupted", os.Getenv("DENSE_MEM_E2E_FEATURE_STORAGE_VARIANT"))
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
	seedFeatureStorageProgress(t, db, 210)
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

func seedFeatureStorageProgress(t testing.TB, db *gorm.DB, count int) {
	t.Helper()
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		var team, shared string
		var generation int64
		if err := tx.Raw(`SELECT team.id::text,space.id::text,space.generation FROM teams team
		 JOIN memory_spaces space ON space.team_id=team.id AND space.kind='team_shared'
		 WHERE team.name='progress-fixture'`).Row().Scan(&team, &shared, &generation); err != nil {
			return err
		}
		publication := uuid.NewString()
		if err := tx.Exec(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation,revision) VALUES(?::uuid,?::uuid,?,1)`, team, shared, generation).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,publication_id,revision,operation_key,request_hash,origin,reason,result)
		 VALUES(?::uuid,?::uuid,?,?::uuid,1,'feature-storage-progress',?,'automatic','feature storage fixture','{}')`, team, shared, generation, publication, "sha256:"+strings.Repeat("a", 64)).Error; err != nil {
			return err
		}
		if err := insertProgressTopics(tx, team, shared, generation, publication, count); err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired)
		 SELECT team_id,shared_space_id,space_generation,record_id,version,kind,retired FROM ontology_record_revisions WHERE team_id=?::uuid AND publication_id=?::uuid`, team, publication).Error
	}))
}
