//go:build integration

package migrationapp

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
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
	require.NoError(t, storage.NewRLS().WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
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
