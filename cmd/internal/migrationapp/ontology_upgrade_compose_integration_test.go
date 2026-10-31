//go:build integration

package migrationapp

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypostgres "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOntologyUpgradeComposeSeed(t *testing.T) {
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
	definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "upgrade-matching", Label: "Upgrade matching", Description: "of and the"}}
	var team string
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT team_id::text FROM ontology_maintenance_batches WHERE batch_id=?::uuid`, batch).Row().Scan(&team)
	}))
	_, err = ontologypostgres.NewStore(db, storage.NewRLS()).PublishAutomatic(context.Background(), team,
		ontology.Publication{OperationKey: "upgrade-matching-definition", Reason: "Preserved matching vocabulary", Changes: []ontology.Change{{Record: definition}}})
	require.NoError(t, err)
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
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
