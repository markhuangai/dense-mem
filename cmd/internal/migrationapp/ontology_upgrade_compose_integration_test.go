//go:build integration

package migrationapp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
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
		t.Skip("ontology upgrade seed requires its dedicated production-image scenario")
	}
	require.Contains(t, []string{"expired", "live", "unreconciled"}, variant)
	dsn := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dsn)
	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	batch := seedOntologyUpgrade(t, db)
	definition := topic.Record{ID: uuid.NewString(), Kind: topic.Topic, Definition: &topic.Definition{Key: "upgrade-matching", Label: "Upgrade matching", Description: "of and the"}}
	definition.Version = 1
	definition.Fingerprint, err = topic.RecordFingerprintWithAcceptedSources(definition, nil)
	require.NoError(t, err)
	body, err := json.Marshal(definition)
	require.NoError(t, err)
	publication := uuid.NewString()
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		var team, shared string
		var generation int64
		if err := tx.Raw(`SELECT team_id::text,shared_space_id::text,space_generation FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, batch).Row().Scan(&team, &shared, &generation); err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_catalog_heads(team_id,shared_space_id,space_generation,revision) VALUES(?,?,?,1)`, team, shared, generation).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_publications(team_id,shared_space_id,space_generation,publication_id,revision,operation_key,request_hash,origin,reason,result)
		     VALUES(?,?,?,?,1,'upgrade-matching-definition',?,'automatic','Preserved matching vocabulary','{}')`, team, shared, generation, publication, definition.Fingerprint).Error; err != nil {
			return err
		}
		if err := tx.Exec(`SELECT dense_mem_insert_ontology_revision(?,?,?,?,?::jsonb)`, team, shared, generation, publication, string(body)).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO ontology_record_heads(team_id,shared_space_id,space_generation,record_id,version,kind,retired,names)
		    VALUES(?,?,?,?,1,'topic',false,ARRAY['upgrade-matching','upgrade matching'])`, team, shared, generation, definition.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM ontology_maintenance_markers WHERE team_id=?::uuid AND anchor_kind='ontology_record_heads' AND anchor_id=?`, team, definition.ID).Error; err != nil {
			return err
		}
		switch variant {
		case "live":
			return tx.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()+interval '30 seconds' WHERE batch_id=?::uuid`, batch).Error
		case "unreconciled":
			return tx.Exec(`INSERT INTO ontology_maintenance_attempts(batch_id,assessment_id,attempt,window_id,reserved_input,reserved_output,admitted_at)
			 SELECT batch_id,?::uuid,1,window_id,20,30,clock_timestamp() FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, uuid.NewString(), batch).Error
		default:
			return nil
		}
	}))
}
